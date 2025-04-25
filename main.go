package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil" // Added for reading error response body
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/elastic/go-elasticsearch/v7"
	"github.com/elastic/go-elasticsearch/v7/esapi"
	"github.com/joho/godotenv"
)

// --- Globals, Config Struct, Time Layouts, Helper Functions ---
// (These remain unchanged from the previous version - Omitted for Brevity)
// ...
var (
	sourceClient *elasticsearch.Client
	targetClient *elasticsearch.Client
)
type Config struct {
	SourceHost       string; SourcePort       int; SourceUser       string; SourcePass       string
	TargetHost       string; TargetPort       int; TargetUser       string; TargetPass       string
	IndexName        string; TargetIndexName  string
	BatchSize        int; Workers          int; SSLVerify        bool; ScrollDuration   time.Duration; MaxRetries       int
	StartTime        *time.Time; EndTime          *time.Time; TimeShiftSeconds int
}
var timeLayouts = []string{ time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05", "2006-01-02", }
func parseTime(timeStr string, forConfig bool) (*time.Time, error) { if timeStr == "" { return nil, nil }; for _, layout := range timeLayouts { t, err := time.Parse(layout, timeStr); if err == nil { if forConfig && layout == "2006-01-02" { log.Printf("Info: Parsed date-only value '%s' using layout '%s'. StartTime implies 00:00:00, EndTime implies comparison '< %s 00:00:00'.", timeStr, layout, timeStr) }; return &t, nil } }; return nil, fmt.Errorf("could not parse time '%s' using supported layouts", timeStr) }
func getEnv(key, fallback string) string { if value, exists := os.LookupEnv(key); exists && value != "" { return value }; return fallback }
func getRequiredEnv(key string) string { value, exists := os.LookupEnv(key); if !exists || value == "" { log.Fatalf("Error: Required environment variable %s is not set or is empty. Please check your .env file.", key) }; return value }

// --- main Function (Unchanged from previous version) ---
func main() {
	err := godotenv.Load(); if err != nil { log.Println("Info: No .env file found or error loading it. Relying on existing environment variables.") }
	cfg := Config{}; var parseErr error
	// Source Config
	cfg.SourceHost = getRequiredEnv("SOURCE_HOST"); sourcePortStr := getRequiredEnv("SOURCE_PORT"); cfg.SourceUser = os.Getenv("SOURCE_USER"); cfg.SourcePass = os.Getenv("SOURCE_PASS"); cfg.SourcePort, parseErr = strconv.Atoi(sourcePortStr); if parseErr != nil { log.Fatalf("Error: Invalid SOURCE_PORT value '%s'. Must be an integer.", sourcePortStr) }
	// Target Config
	targetHostStr := os.Getenv("TARGET_HOST"); targetPortStr := os.Getenv("TARGET_PORT"); targetUserStr := os.Getenv("TARGET_USER"); targetPassStr := os.Getenv("TARGET_PASS")
	// Cloning Config
	cfg.IndexName = getRequiredEnv("INDEX_NAME"); cfg.TargetIndexName = os.Getenv("TARGET_INDEX_NAME"); batchSizeStr := getEnv("BATCH_SIZE", "1000"); workersStr := getEnv("WORKERS", "4"); sslVerifyStr := getEnv("SSL_VERIFY", "true"); scrollDurationStr := getEnv("SCROLL_DURATION", "1m"); maxRetriesStr := getEnv("MAX_RETRIES", "3")
	// Time Manipulation Config
	startTimeStr := os.Getenv("START_TIME"); endTimeStr := os.Getenv("END_TIME"); timeShiftStr := getEnv("TIME_SHIFT_SECONDS", "0")
	// Apply Defaulting Logic for Target Connection
	if targetHostStr == "" { log.Println("Info: TARGET_HOST not set, defaulting to SOURCE_HOST."); cfg.TargetHost = cfg.SourceHost } else { cfg.TargetHost = targetHostStr }
	if targetPortStr == "" { log.Println("Info: TARGET_PORT not set, defaulting to SOURCE_PORT."); cfg.TargetPort = cfg.SourcePort } else { cfg.TargetPort, parseErr = strconv.Atoi(targetPortStr); if parseErr != nil { log.Fatalf("Error: Invalid TARGET_PORT value '%s'. Must be an integer.", targetPortStr) } }
	if targetUserStr == "" { if cfg.SourceUser != "" { log.Println("Info: TARGET_USER not set, defaulting to SOURCE_USER.") }; cfg.TargetUser = cfg.SourceUser } else { cfg.TargetUser = targetUserStr }
	if targetPassStr == "" { if cfg.SourcePass != "" { log.Println("Info: TARGET_PASS not set, defaulting to SOURCE_PASS.") }; cfg.TargetPass = cfg.SourcePass } else { cfg.TargetPass = targetPassStr }
	// Parse Remaining Numeric/Boolean/Duration/Time/Int
	cfg.BatchSize, parseErr = strconv.Atoi(batchSizeStr); if parseErr != nil || cfg.BatchSize <= 0 { log.Printf("Warning: Invalid BATCH_SIZE value '%s'. Using default 1000.", batchSizeStr); cfg.BatchSize = 1000 }
	cfg.Workers, parseErr = strconv.Atoi(workersStr); if parseErr != nil || cfg.Workers <= 0 { log.Printf("Warning: Invalid WORKERS value '%s'. Using default 4.", workersStr); cfg.Workers = 4 }
	cfg.SSLVerify, parseErr = strconv.ParseBool(sslVerifyStr); if parseErr != nil { log.Printf("Warning: Invalid SSL_VERIFY value '%s'. Using default true.", sslVerifyStr); cfg.SSLVerify = true }
	cfg.ScrollDuration, parseErr = time.ParseDuration(scrollDurationStr); if parseErr != nil { log.Printf("Warning: Invalid SCROLL_DURATION value '%s'. Using default 1m.", scrollDurationStr); cfg.ScrollDuration = 1 * time.Minute }
	cfg.MaxRetries, parseErr = strconv.Atoi(maxRetriesStr); if parseErr != nil || cfg.MaxRetries < 0 { log.Printf("Warning: Invalid MAX_RETRIES value '%s'. Using default 3.", maxRetriesStr); cfg.MaxRetries = 3 }
	// Parse Start/End Times
	cfg.StartTime, err = parseTime(startTimeStr, true); if err != nil { log.Printf("Warning: Could not parse START_TIME '%s': %v. Time filtering disabled for start time.", startTimeStr, err) } else if cfg.StartTime != nil { log.Printf("Info: Parsed START_TIME: %s", cfg.StartTime.Format(time.RFC3339)) }
	cfg.EndTime, err = parseTime(endTimeStr, true); if err != nil { log.Printf("Warning: Could not parse END_TIME '%s': %v. Time filtering disabled for end time.", endTimeStr, err) } else if cfg.EndTime != nil { log.Printf("Info: Parsed END_TIME: %s", cfg.EndTime.Format(time.RFC3339)) }
	// Parse Time Shift
	cfg.TimeShiftSeconds, parseErr = strconv.Atoi(timeShiftStr); if parseErr != nil { log.Printf("Warning: Invalid TIME_SHIFT_SECONDS value '%s'. Must be an integer. Disabling time shift.", timeShiftStr); cfg.TimeShiftSeconds = 0 } else if cfg.TimeShiftSeconds != 0 { log.Printf("Info: Parsed TIME_SHIFT_SECONDS: %d", cfg.TimeShiftSeconds) }
	// Initialize Elasticsearch Clients
	sourceClient, err = createEsClient("Source", cfg.SourceHost, cfg.SourcePort, cfg.SourceUser, cfg.SourcePass, cfg.SSLVerify, cfg.MaxRetries); if err != nil { log.Fatalf("Error creating source Elasticsearch client: %v", err) }
	targetClient, err = createEsClient("Target", cfg.TargetHost, cfg.TargetPort, cfg.TargetUser, cfg.TargetPass, cfg.SSLVerify, cfg.MaxRetries); if err != nil { log.Fatalf("Error creating target Elasticsearch client: %v", err) }
	log.Println("Source and target Elasticsearch clients initialized."); processIndices(cfg); log.Println("Cloning process finished.")
}


// --- Utility Functions (createEsClient, getSourceIndices, hasTimestampField, getIndexMetadataAndCheckTimestamp, cleanSettings, createIndexWithMetadata, buildQuery, processScrollResponse, extractID, extractSource, clearScroll, refreshIndex) ---
// (Paste the unchanged utility functions from the previous response here) ...
// Creates an Elasticsearch client (same as before)
func createEsClient(clientType, host string, port int, user, pass string, sslVerify bool, maxRetries int) (*elasticsearch.Client, error) { address := fmt.Sprintf("http://%s:%d", host, port); scheme := "http"; if port == 443 || sslVerify || (user != "" && pass != "") { address = fmt.Sprintf("https://%s:%d", host, port); scheme = "https" }; esCfg := elasticsearch.Config{ Addresses: []string{address}, Username: user, Password: pass, RetryOnStatus: []int{502, 503, 504, 429}, MaxRetries: maxRetries, RetryBackoff: func(i int) time.Duration { return time.Duration(1<<uint(i)) * time.Second }, Transport: &http.Transport{ MaxIdleConnsPerHost: 10, ResponseHeaderTimeout: time.Second * 30, TLSClientConfig: &tls.Config{ InsecureSkipVerify: !sslVerify, }, }, }; log.Printf("Info: [%s Client] Attempting connection to %s", clientType, address); if !sslVerify && scheme == "https" { log.Printf("Warning: [%s Client] SSL verification is disabled (SSL_VERIFY=false)", clientType) }; esClient, err := elasticsearch.NewClient(esCfg); if err != nil { return nil, fmt.Errorf("[%s Client] error creating client: %w", clientType, err) }; res, err := esClient.Info(); if err != nil { return nil, fmt.Errorf("[%s Client] cannot connect to %s: %w", clientType, address, err) }; defer res.Body.Close(); if res.IsError() { return nil, fmt.Errorf("[%s Client] connection error to %s: %s", clientType, address, res.String()) }; log.Printf("[%s Client] Successfully connected to %s", clientType, address); return esClient, nil }
// Gets indices matching the pattern from the source (same as before)
func getSourceIndices(pattern string) ([]string, error) { res, err := sourceClient.Cat.Indices(sourceClient.Cat.Indices.WithIndex(pattern), sourceClient.Cat.Indices.WithFormat("json"), sourceClient.Cat.Indices.WithH("index")); if err != nil { return nil, fmt.Errorf("cannot get source indices for pattern '%s': %w", pattern, err) }; defer res.Body.Close(); if res.IsError() { return nil, fmt.Errorf("error response when getting source indices for pattern '%s': %s", pattern, res.String()) }; var indicesInfo []map[string]interface{}; if err := json.NewDecoder(res.Body).Decode(&indicesInfo); err != nil { return nil, fmt.Errorf("error decoding source indices response: %w", err) }; var indices []string; for _, info := range indicesInfo { if indexName, ok := info["index"].(string); ok { indices = append(indices, indexName) } }; if len(indices) == 0 { log.Printf("Warning: No source indices found matching pattern: %s", pattern) } else { log.Printf("Found source indices matching '%s': %v", pattern, indices) }; return indices, nil }
// Checks if "@timestamp" exists in the mapping properties (same as before)
func hasTimestampField(mappings map[string]interface{}) bool { if mappings == nil { return false }; if properties, ok := mappings["properties"].(map[string]interface{}); ok { _, exists := properties["@timestamp"]; return exists }; return false }
// Gets mapping and settings for a given index AND checks for @timestamp field (same as before)
func getIndexMetadataAndCheckTimestamp(client *elasticsearch.Client, indexName string) (map[string]interface{}, map[string]interface{}, bool, error) { res, err := client.Indices.Get([]string{indexName}, client.Indices.Get.WithContext(context.Background())); if err != nil { return nil, nil, false, fmt.Errorf("error getting index metadata request: %w", err) }; defer res.Body.Close(); if res.IsError() { return nil, nil, false, fmt.Errorf("error response getting index metadata: %s", res.String()) }; var response map[string]interface{}; if err := json.NewDecoder(res.Body).Decode(&response); err != nil { return nil, nil, false, fmt.Errorf("error decoding index metadata response: %w", err) }; indexData, ok := response[indexName].(map[string]interface{}); if !ok { return nil, nil, false, fmt.Errorf("unexpected structure in index metadata response for '%s'", indexName) }; mappings, _ := indexData["mappings"].(map[string]interface{}); settingsData, _ := indexData["settings"].(map[string]interface{}); cleanedSettings := cleanSettings(settingsData); timestampExists := hasTimestampField(mappings); return mappings, cleanedSettings, timestampExists, nil }
// Cleans settings obtained from the source index (same as before)
func cleanSettings(settingsData map[string]interface{}) map[string]interface{} { if settingsData == nil { return nil }; indexSettings, ok := settingsData["index"].(map[string]interface{}); if !ok { log.Println("Warning: Could not find 'index' key in settings, returning settings as is."); return settingsData }; keysToRemove := []string{ "creation_date", "uuid", "version", "provided_name", "created", "routing.allocation.include._tier_preference", }; cleanedIndexSettings := make(map[string]interface{}); for key, value := range indexSettings { remove := false; for _, removeKey := range keysToRemove { if key == removeKey { remove = true; break } }; if key == "version" { remove = true }; if !remove { cleanedIndexSettings[key] = value } }; return map[string]interface{}{ "index": cleanedIndexSettings, } }
// Creates an index on the target client (same as before)
func createIndexWithMetadata(client *elasticsearch.Client, targetIndexName string, mapping map[string]interface{}, settings map[string]interface{}) error { createBody := map[string]interface{}{}; if mapping != nil && len(mapping) > 0 { createBody["mappings"] = mapping }; if settings != nil { if indexSettings, ok := settings["index"]; ok && len(indexSettings.(map[string]interface{})) > 0 { createBody["settings"] = settings } else if !ok && len(settings) > 0 { log.Println("Info: Wrapping non-standard settings under 'index' key for create index request."); createBody["settings"] = map[string]interface{}{"index": settings} } }; if len(createBody) == 0 { log.Printf("Info: No mapping or transferable settings found/provided for target index %s. Creating with defaults.", targetIndexName) }; bodyBytes, err := json.Marshal(createBody); if err != nil { return fmt.Errorf("error marshaling create index request body: %w", err) }; log.Printf("Creating target index %s with mappings and settings...", targetIndexName); res, err := client.Indices.Create(targetIndexName, client.Indices.Create.WithContext(context.Background()), client.Indices.Create.WithBody(bytes.NewReader(bodyBytes)), ); if err != nil { return fmt.Errorf("error in create index request for %s: %w", targetIndexName, err) }; defer res.Body.Close(); if res.IsError() { return fmt.Errorf("error response creating index %s: %s", targetIndexName, res.String()) }; var createResponse map[string]interface{}; if err := json.NewDecoder(res.Body).Decode(&createResponse); err != nil { log.Printf("Warning: Could not decode create index response for %s: %v", targetIndexName, err) } else { acknowledged, _ := createResponse["acknowledged"].(bool); if !acknowledged { log.Printf("Warning: Create index request for %s was not acknowledged by all nodes.", targetIndexName) } }; return nil }
// Builds the Elasticsearch query based on time filters and @timestamp presence (same as before)
func buildQuery(startTime, endTime *time.Time, timestampFieldExists bool) (string, bool) { applyTimeFilter := timestampFieldExists && (startTime != nil || endTime != nil); if !applyTimeFilter { return `{"query": {"match_all": {}}}`, false }; rangeQuery := map[string]interface{}{}; if startTime != nil { rangeQuery["gte"] = startTime.UTC().Format(time.RFC3339) }; if endTime != nil { rangeQuery["lt"] = endTime.UTC().Format(time.RFC3339) }; query := map[string]interface{}{ "query": map[string]interface{}{ "bool": map[string]interface{}{ "filter": []map[string]interface{}{ {"range": map[string]interface{}{"@timestamp": rangeQuery}}, }, }, }, }; queryBytes, err := json.Marshal(query); if err != nil { log.Printf("Error marshaling time range query: %v. Falling back to match_all.", err); return `{"query": {"match_all": {}}}`, false }; log.Printf("Applying time filter query: @timestamp >= %v AND @timestamp < %v", startTime, endTime); return string(queryBytes), true }
// Processes a scroll response (same as before)
func processScrollResponse(res *esapi.Response, docChan chan<- json.RawMessage) (string, int64, int, error) { defer res.Body.Close(); if res.IsError() { return "", 0, 0, fmt.Errorf("scroll response error: %s", res.String()) }; var r map[string]interface{}; if err := json.NewDecoder(res.Body).Decode(&r); err != nil { return "", 0, 0, fmt.Errorf("error decoding scroll response: %w", err) }; newScrollID, _ := r["_scroll_id"].(string); docsInThisBatch := 0; var totalHits int64; if hitsData, ok := r["hits"].(map[string]interface{}); ok { if total, ok := hitsData["total"].(map[string]interface{}); ok { if value, ok := total["value"].(float64); ok { totalHits = int64(value) } }; if hits, ok := hitsData["hits"].([]interface{}); ok { docsInThisBatch = len(hits); for _, hit := range hits { hitMap, ok := hit.(map[string]interface{}); if !ok { continue }; doc := map[string]interface{}{ "_index": hitMap["_index"], "_id": hitMap["_id"], "_source": hitMap["_source"], }; docBytes, err := json.Marshal(doc); if err != nil { continue }; docChan <- json.RawMessage(docBytes) } } }; if newScrollID == "" && docsInThisBatch > 0{ log.Println("Warning: Received non-empty batch but no scroll ID in response. Assuming scroll ended.") }; return newScrollID, totalHits, docsInThisBatch, nil }
// Helper to extract _id (same as before)
func extractID(docBytes json.RawMessage) string { var doc map[string]interface{}; if err := json.Unmarshal(docBytes, &doc); err == nil { if id, ok := doc["_id"].(string); ok { return id } }; return "" }
// Helper to extract _source (same as before)
func extractSource(docBytes json.RawMessage) json.RawMessage { var doc map[string]interface{}; if err := json.Unmarshal(docBytes, &doc); err == nil { if source, ok := doc["_source"]; ok { sourceBytes, err := json.Marshal(source); if err == nil { return json.RawMessage(sourceBytes) } } }; log.Println("Warning: Could not extract _source from document"); return json.RawMessage("{}") }
// Clears a scroll context on the source cluster (same as before)
func clearScroll(client *elasticsearch.Client, scrollID string) { if scrollID == "" { return }; res, err := client.ClearScroll(client.ClearScroll.WithScrollID(scrollID), client.ClearScroll.WithContext(context.Background())); if err != nil { log.Printf("Warning: Failed to clear scroll context %s: %v", scrollID, err) } else if res != nil { defer res.Body.Close(); if res.IsError() {} } }
// Refreshes the target index to make changes visible (same as before)
func refreshIndex(client *elasticsearch.Client, targetIndexName string) error { log.Printf("Refreshing target index: %s", targetIndexName); res, err := client.Indices.Refresh(client.Indices.Refresh.WithIndex(targetIndexName), client.Indices.Refresh.WithContext(context.Background()), ); if err != nil { return fmt.Errorf("refresh request failed for index %s: %w", targetIndexName, err) }; defer res.Body.Close(); if res.IsError() { return fmt.Errorf("error response refreshing index %s: %s", targetIndexName, res.String()) }; return nil }


// --- Modified Functions ---

// Processes indices, gets metadata, and calls cloneIndex (Unchanged)
func processIndices(appCfg Config) {
	sourceIndices, err := getSourceIndices(appCfg.IndexName)
	if err != nil { log.Fatalf("Failed to get source indices: %v", err) }
	if len(sourceIndices) == 0 { log.Fatalf("No indices found matching source pattern '%s'. Aborting.", appCfg.IndexName) }
	targetIndexNameOverride := appCfg.TargetIndexName
	if targetIndexNameOverride != "" && len(sourceIndices) > 1 { log.Fatalf("Error: TARGET_INDEX_NAME ('%s') is specified, but the source pattern '%s' matched multiple indices (%v). TARGET_INDEX_NAME can only be used when the source pattern matches exactly one index.", targetIndexNameOverride, appCfg.IndexName, sourceIndices) }
	for _, sourceIndexName := range sourceIndices {
		targetIndexName := sourceIndexName
		if targetIndexNameOverride != "" && len(sourceIndices) == 1 { targetIndexName = targetIndexNameOverride; log.Printf("Info: Using specified target index name '%s' for source index '%s'", targetIndexName, sourceIndexName) }
		log.Printf("--- Starting clone: [%s] -> [%s] ---", sourceIndexName, targetIndexName)
		mapping, settings, timestampFieldExists, err := getIndexMetadataAndCheckTimestamp(sourceClient, sourceIndexName)
		if err != nil { log.Printf("Error getting metadata/checking timestamp for source index %s: %v. Skipping.", sourceIndexName, err); continue }
		log.Printf("Retrieved mapping and settings for source index: %s. @timestamp field exists: %t", sourceIndexName, timestampFieldExists)
		cloneIndex(sourceIndexName, targetIndexName, mapping, settings, timestampFieldExists, appCfg) // Pass sourceIndexName
		log.Printf("--- Finished clone: [%s] -> [%s] ---", sourceIndexName, targetIndexName)
	}
}

// Clones a single source index to a target index name (Unchanged)
func cloneIndex(sourceIndexName, targetIndexName string, mapping, settings map[string]interface{}, timestampFieldExists bool, cfg Config) {
	err := createIndexWithMetadata(targetClient, targetIndexName, mapping, settings)
	if err != nil {
		if strings.Contains(err.Error(), "resource_already_exists_exception") { log.Printf("Warning: Target index %s already exists. Skipping creation, will proceed to data copy.", targetIndexName) } else { log.Printf("Error creating target index %s: %v. Skipping index.", targetIndexName, err); return }
	} else { log.Printf("Successfully created target index: %s", targetIndexName) }
	// Pass sourceIndexName down
	err = scrollAndBulkIndex(sourceIndexName, targetIndexName, timestampFieldExists, cfg)
	if err != nil { log.Printf("Error during data copy for index %s -> %s: %v", sourceIndexName, targetIndexName, err) } else { log.Printf("Data copy finished for index %s -> %s", sourceIndexName, targetIndexName) }
	err = refreshIndex(targetClient, targetIndexName); if err != nil { log.Printf("Warning: Failed to refresh target index %s: %v", targetIndexName, err) } else { log.Printf("Target index %s refreshed.", targetIndexName) }
}

// Scrolls through source index and bulk indexes data (Unchanged)
func scrollAndBulkIndex(sourceIndexName, targetIndexName string, timestampFieldExists bool, cfg Config) error {
	ctx := context.Background()
	var wg sync.WaitGroup
	docChan := make(chan json.RawMessage, cfg.BatchSize*(cfg.Workers+1))
	queryBody, timeFilterApplied := buildQuery(cfg.StartTime, cfg.EndTime, timestampFieldExists)
	if timeFilterApplied { log.Printf("Starting scroll for source index %s with time filter and batch size %d", sourceIndexName, cfg.BatchSize) } else { log.Printf("Starting scroll for source index %s (no time filter) with batch size %d", sourceIndexName, cfg.BatchSize) }
	res, err := sourceClient.Search(sourceClient.Search.WithIndex(sourceIndexName), sourceClient.Search.WithSize(cfg.BatchSize), sourceClient.Search.WithScroll(cfg.ScrollDuration), sourceClient.Search.WithContext(ctx), sourceClient.Search.WithBody(strings.NewReader(queryBody)), sourceClient.Search.WithSort("_doc"))
	if err != nil { return fmt.Errorf("initial scroll request failed for %s: %w", sourceIndexName, err) }
	var scrollID string; var totalHits int64; var docsInBatch int
	scrollID, totalHits, docsInBatch, err = processScrollResponse(res, docChan); if err != nil { return fmt.Errorf("processing initial scroll batch failed for %s: %w", sourceIndexName, err) }
	if totalHits == 0 && docsInBatch == 0 { log.Printf("Info: Source index %s contains no documents matching the criteria.", sourceIndexName); close(docChan); clearScroll(sourceClient, scrollID); return nil }
	if timeFilterApplied { log.Printf("Processing documents matching time filter for index %s (initial total estimate: %d)", sourceIndexName, totalHits) } else { log.Printf("Total documents to process for index %s: %d", sourceIndexName, totalHits) }
	processedCount := int64(docsInBatch)
	for i := 0; i < cfg.Workers; i++ { wg.Add(1); go bulkWorker(i, sourceIndexName, targetIndexName, docChan, &wg, timestampFieldExists, cfg) } // Pass sourceIndexName
	for {
		log.Printf("Processed approx %d documents for %s...", processedCount, sourceIndexName)
		res, err := sourceClient.Scroll(sourceClient.Scroll.WithScrollID(scrollID), sourceClient.Scroll.WithScroll(cfg.ScrollDuration), sourceClient.Scroll.WithContext(ctx))
		if err != nil { clearScroll(sourceClient, scrollID); return fmt.Errorf("subsequent scroll request failed for %s: %w", sourceIndexName, err) }
		scrollID, _, docsInBatch, err = processScrollResponse(res, docChan); if err != nil { clearScroll(sourceClient, scrollID); return fmt.Errorf("processing subsequent scroll batch failed for %s: %w", sourceIndexName, err) }
		processedCount += int64(docsInBatch)
		if docsInBatch == 0 { log.Printf("Scroll finished for %s (empty batch received).", sourceIndexName); break }
		if scrollID == "" { log.Printf("Scroll finished for %s (empty scroll ID received).", sourceIndexName); break }
	}
	if scrollID != "" { clearScroll(sourceClient, scrollID) }
	log.Println("Finished scrolling, closing document channel for", sourceIndexName); close(docChan)
	log.Println("Waiting for bulk workers to finish for index", sourceIndexName); wg.Wait()
	log.Println("All bulk workers finished for index", sourceIndexName); return nil
}


// **** Worker goroutine for bulk indexing - REVISED ****
func bulkWorker(id int, sourceIndexName, targetIndexName string, docChan <-chan json.RawMessage, wg *sync.WaitGroup, timestampFieldExists bool, cfg Config) {
	defer wg.Done()
	log.Printf("[Worker %d] Starting for source [%s] -> target [%s]", id, sourceIndexName, targetIndexName)

	var buffer bytes.Buffer
	var docsInBatch int
	// Determine conditions for time shifting and potential duplication upfront
	shouldAttemptTimeShift := timestampFieldExists && cfg.TimeShiftSeconds != 0
	isSameIndexOperation := sourceIndexName == targetIndexName

	for docBytes := range docChan {
		docID := extractID(docBytes)
		originalSourceBytes := extractSource(docBytes) // Get original source
		finalSourceBytes := originalSourceBytes        // Start with original, modify if needed
		shiftAppliedSuccessfully := false            // Track if THIS doc was shifted

		// --- Apply Time Shift Logic ---
		if shouldAttemptTimeShift {
			var sourceMap map[string]interface{}
			if err := json.Unmarshal(originalSourceBytes, &sourceMap); err == nil {
				if tsValue, tsExists := sourceMap["@timestamp"]; tsExists {
					if tsStr, ok := tsValue.(string); ok {
						originalTime, err := parseTime(tsStr, false)
						if err == nil && originalTime != nil {
							shiftedTime := originalTime.Add(time.Duration(cfg.TimeShiftSeconds) * time.Second)
							sourceMap["@timestamp"] = shiftedTime.UTC().Format(time.RFC3339Nano) // Update map
							modifiedSourceBytes, err := json.Marshal(sourceMap)
							if err == nil {
								finalSourceBytes = modifiedSourceBytes // Use modified source
								shiftAppliedSuccessfully = true     // Mark shift successful for THIS doc
							} else {
								log.Printf("[Worker %d] Warning: Failed to re-marshal source after time shift for doc %s: %v", id, docID, err)
							}
						} // else: parsing @timestamp failed for this doc
					} // else: @timestamp not a string
				} // else: @timestamp field not in this doc
			} else {
				log.Printf("[Worker %d] Warning: Failed to unmarshal source for time shift check for doc %s: %v", id, docID, err)
			}
		}
		// --- End Time Shift Logic ---

		// --- Prepare Bulk Metadata (Conditionally omit _id) ---
		actionMetadata := map[string]interface{}{
			"_index": targetIndexName,
		}

		// ** FIX **: Omit _id only if:
		// 1. Writing back to the *same index*
		// 2. Time shifting is generally enabled for this run
		// 3. Time shifting was *successfully applied* to this specific document
		// This ensures we only duplicate the successfully shifted docs, not failed ones or original ones.
		if isSameIndexOperation && shouldAttemptTimeShift && shiftAppliedSuccessfully {
			// Omit _id to force document creation (duplication)
			log.Printf("[Worker %d] Omitting _id for successfully time-shifted duplicate doc in index [%s]", id, targetIndexName)
		} else {
			// Keep original _id for overwriting target (if different index) OR
			// for overwriting target (if same index but NO time shift applied/enabled) OR
			// for overwriting target (if same index and time shift FAILED for this doc)
			actionMetadata["_id"] = docID
		}

		meta := map[string]interface{}{
			"index": actionMetadata,
		}
		metaBytes, _ := json.Marshal(meta)

		// Append metadata and the final source bytes (original or shifted)
		buffer.Write(metaBytes)
		buffer.WriteByte('\n')
		buffer.Write(finalSourceBytes)
		buffer.WriteByte('\n')

		docsInBatch++

		if docsInBatch >= cfg.BatchSize {
			sendBulkRequest(id, targetIndexName, &buffer) // Pass buffer by pointer
			buffer.Reset()
			docsInBatch = 0
		}
	}

	// Send remaining items
	if buffer.Len() > 0 {
		log.Printf("[Worker %d] Sending final batch of %d docs to %s", id, docsInBatch, targetIndexName)
		sendBulkRequest(id, targetIndexName, &buffer) // Pass buffer by pointer
	}

	log.Printf("[Worker %d] Finished for target index %s", id, targetIndexName)
}


// Sends a bulk request to the target cluster for a specific index
// Added more detailed error logging
func sendBulkRequest(workerID int, targetIndexName string, buffer *bytes.Buffer) {
	if buffer.Len() == 0 {
		return
	}

	req := esapi.BulkRequest{
		Body:    bytes.NewReader(buffer.Bytes()),
		Index:   targetIndexName, // Specifying index here might help ES routing, but metadata action overrides it
		Refresh: "false",         // Explicitly disable refresh on each bulk for performance
	}

	res, err := req.Do(context.Background(), targetClient) // Use target client

	if err != nil {
		log.Printf("[Worker %d] ERROR sending bulk request to %s: %v", workerID, targetIndexName, err)
		// Consider adding retry logic here based on error type
		return
	}
	defer res.Body.Close()

	// Check for HTTP errors first
	if res.IsError() {
		bodyBytes, _ := ioutil.ReadAll(res.Body) // Read body for details
		log.Printf("[Worker %d] ERROR response (%s) from bulk request to %s: %s", workerID, res.Status(), targetIndexName, string(bodyBytes))

	} else {
		// Decode successful response to check for item-level errors
		var bulkResponse map[string]interface{}
		if err := json.NewDecoder(res.Body).Decode(&bulkResponse); err == nil {
			if errors, ok := bulkResponse["errors"].(bool); ok && errors {
				log.Printf("[Worker %d] WARNING: Bulk request to %s completed WITH item-level errors. Sample errors:", workerID, targetIndexName)
				// Log first few item errors for diagnosis
				items, _ := bulkResponse["items"].([]interface{})
				errorCount := 0
				for _, item := range items {
					if errorCount >= 5 { break } // Limit logged errors
					itemMap, ok := item.(map[string]interface{})
					if !ok { continue }
					actionResult, ok := itemMap["index"].(map[string]interface{}) // Check "index" action
					if !ok { continue }

					if errorInfo, exists := actionResult["error"]; exists {
						log.Printf("  - Item Error: ID=%v, Status=%v, Error=%v",
							actionResult["_id"],
							actionResult["status"],
							errorInfo)
						errorCount++
					}
				}
			} else {
				// Success, potentially log batch size completion? Can be noisy.
				log.Printf("[Worker %d] Bulk request successful to %s.", workerID, targetIndexName)
			}
		} else {
			log.Printf("[Worker %d] Warning: Could not decode successful bulk response body for %s: %v", workerID, targetIndexName, err)
		}
	}
}