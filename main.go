package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
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

// Global Elasticsearch clients
var (
	sourceClient *elasticsearch.Client
	targetClient *elasticsearch.Client
)

// Configuration struct
type Config struct {
	SourceHost      string
	SourcePort      int
	SourceUser      string
	SourcePass      string
	TargetHost      string
	TargetPort      int
	TargetUser      string
	TargetPass      string
	IndexName       string
	TargetIndexName string // New field for optional target index name
	BatchSize       int
	Workers         int
	SSLVerify       bool
	ScrollDuration  time.Duration
	MaxRetries      int
}

// Helper function to get environment variables with defaults and logging
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists && value != "" { // Treat empty string as not set for defaulting
		return value
	}
	if fallback != "" {
		// Only log default usage if a fallback value is actually provided and used
		// Avoid logging for optional fields where empty is acceptable before defaulting logic
		// log.Printf("Warning: Environment variable %s not set, using default: %s", key, fallback)
	} else {
		// log.Printf("Info: Environment variable %s not set, no default provided.", key)
	}
	return fallback
}

// Helper function to get required environment variables, exiting if missing
func getRequiredEnv(key string) string {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		log.Fatalf("Error: Required environment variable %s is not set or is empty. Please check your .env file.", key)
	}
	return value
}

func main() {
	// Load .env file.
	err := godotenv.Load()
	if err != nil {
		log.Println("Info: No .env file found or error loading it. Relying on existing environment variables.")
	}

	// --- Load Configuration ---
	cfg := Config{}
	var parseErr error

	// Source Config (Required)
	cfg.SourceHost = getRequiredEnv("SOURCE_HOST")
	sourcePortStr := getRequiredEnv("SOURCE_PORT")
	cfg.SourceUser = os.Getenv("SOURCE_USER")
	cfg.SourcePass = os.Getenv("SOURCE_PASS")
	cfg.SourcePort, parseErr = strconv.Atoi(sourcePortStr)
	if parseErr != nil {
		log.Fatalf("Error: Invalid SOURCE_PORT value '%s'. Must be an integer.", sourcePortStr)
	}

	// Target Config (Optional - Defaulting Logic Applied Below)
	targetHostStr := os.Getenv("TARGET_HOST")
	targetPortStr := os.Getenv("TARGET_PORT")
	targetUserStr := os.Getenv("TARGET_USER")
	targetPassStr := os.Getenv("TARGET_PASS")

	// Cloning Config
	cfg.IndexName = getRequiredEnv("INDEX_NAME")
	cfg.TargetIndexName = os.Getenv("TARGET_INDEX_NAME") // Optional target index name
	batchSizeStr := getEnv("BATCH_SIZE", "1000")
	workersStr := getEnv("WORKERS", "4")
	sslVerifyStr := getEnv("SSL_VERIFY", "true")
	scrollDurationStr := getEnv("SCROLL_DURATION", "1m")
	maxRetriesStr := getEnv("MAX_RETRIES", "3")

	// --- Apply Defaulting Logic for Target Connection ---
	if targetHostStr == "" {
		log.Println("Info: TARGET_HOST not set, defaulting to SOURCE_HOST.")
		cfg.TargetHost = cfg.SourceHost
	} else {
		cfg.TargetHost = targetHostStr
	}

	if targetPortStr == "" {
		log.Println("Info: TARGET_PORT not set, defaulting to SOURCE_PORT.")
		cfg.TargetPort = cfg.SourcePort // Default to already parsed source port
	} else {
		// Parse target port if provided
		cfg.TargetPort, parseErr = strconv.Atoi(targetPortStr)
		if parseErr != nil {
			log.Fatalf("Error: Invalid TARGET_PORT value '%s'. Must be an integer.", targetPortStr)
		}
	}

	if targetUserStr == "" {
		// Only log if source user was actually set
		if cfg.SourceUser != "" {
			log.Println("Info: TARGET_USER not set, defaulting to SOURCE_USER.")
		}
		cfg.TargetUser = cfg.SourceUser
	} else {
		cfg.TargetUser = targetUserStr
	}

	if targetPassStr == "" {
		// Only log if source pass was actually set
		if cfg.SourcePass != "" {
			log.Println("Info: TARGET_PASS not set, defaulting to SOURCE_PASS.")
		}
		cfg.TargetPass = cfg.SourcePass
	} else {
		cfg.TargetPass = targetPassStr
	}

	// --- Parse Remaining Numeric/Boolean/Duration ---
	cfg.BatchSize, parseErr = strconv.Atoi(batchSizeStr)
	if parseErr != nil || cfg.BatchSize <= 0 {
		log.Printf("Warning: Invalid BATCH_SIZE value '%s'. Using default 1000.", batchSizeStr)
		cfg.BatchSize = 1000
	}
	cfg.Workers, parseErr = strconv.Atoi(workersStr)
	if parseErr != nil || cfg.Workers <= 0 {
		log.Printf("Warning: Invalid WORKERS value '%s'. Using default 4.", workersStr)
		cfg.Workers = 4
	}
	cfg.SSLVerify, parseErr = strconv.ParseBool(sslVerifyStr)
	if parseErr != nil {
		log.Printf("Warning: Invalid SSL_VERIFY value '%s'. Using default true.", sslVerifyStr)
		cfg.SSLVerify = true
	}
	cfg.ScrollDuration, parseErr = time.ParseDuration(scrollDurationStr)
	if parseErr != nil {
		log.Printf("Warning: Invalid SCROLL_DURATION value '%s'. Using default 1m.", scrollDurationStr)
		cfg.ScrollDuration = 1 * time.Minute
	}
	cfg.MaxRetries, parseErr = strconv.Atoi(maxRetriesStr)
	if parseErr != nil || cfg.MaxRetries < 0 {
		log.Printf("Warning: Invalid MAX_RETRIES value '%s'. Using default 3.", maxRetriesStr)
		cfg.MaxRetries = 3
	}

	// --- Initialize Elasticsearch Clients ---
	// Note: SSLVerify applies to both clients if set
	sourceClient, err = createEsClient("Source", cfg.SourceHost, cfg.SourcePort, cfg.SourceUser, cfg.SourcePass, cfg.SSLVerify, cfg.MaxRetries)
	if err != nil {
		log.Fatalf("Error creating source Elasticsearch client: %v", err)
	}
	targetClient, err = createEsClient("Target", cfg.TargetHost, cfg.TargetPort, cfg.TargetUser, cfg.TargetPass, cfg.SSLVerify, cfg.MaxRetries)
	if err != nil {
		log.Fatalf("Error creating target Elasticsearch client: %v", err)
	}

	log.Println("Source and target Elasticsearch clients initialized.")
	log.Printf("Source: %s:%d", cfg.SourceHost, cfg.SourcePort)
	log.Printf("Target: %s:%d", cfg.TargetHost, cfg.TargetPort)
	log.Printf("Attempting to clone source index pattern: %s", cfg.IndexName)
	if cfg.TargetIndexName != "" {
		log.Printf("Will attempt to clone to target index name: %s (only if source pattern matches exactly one index)", cfg.TargetIndexName)
	}
	log.Printf("Batch size: %d, Workers: %d", cfg.BatchSize, cfg.Workers)

	// --- Start Cloning Process ---
	processIndices(cfg)

	log.Println("Cloning process finished.")
}

// Creates an Elasticsearch client
func createEsClient(clientType, host string, port int, user, pass string, sslVerify bool, maxRetries int) (*elasticsearch.Client, error) {
	address := fmt.Sprintf("http://%s:%d", host, port) // Default to http
	scheme := "http"

	// Determine scheme based on port, explicit SSL verification, or credentials
	if port == 443 || sslVerify || (user != "" && pass != "") {
		address = fmt.Sprintf("https://%s:%d", host, port)
		scheme = "https"
	}

	esCfg := elasticsearch.Config{
		Addresses:     []string{address},
		Username:      user,
		Password:      pass,
		RetryOnStatus: []int{502, 503, 504, 429},
		MaxRetries:    maxRetries,
		RetryBackoff: func(i int) time.Duration {
			return time.Duration(1<<uint(i)) * time.Second
		},
		Transport: &http.Transport{
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: time.Second * 30,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: !sslVerify,
			},
		},
	}

	log.Printf("Info: [%s Client] Attempting connection to %s", clientType, address)
	if !sslVerify && scheme == "https" {
		log.Printf("Warning: [%s Client] SSL verification is disabled (SSL_VERIFY=false)", clientType)
	}

	esClient, err := elasticsearch.NewClient(esCfg)
	if err != nil {
		return nil, fmt.Errorf("[%s Client] error creating client: %w", clientType, err)
	}

	// Test connection
	res, err := esClient.Info()
	if err != nil {
		return nil, fmt.Errorf("[%s Client] cannot connect to %s: %w", clientType, address, err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, fmt.Errorf("[%s Client] connection error to %s: %s", clientType, address, res.String())
	}
	log.Printf("[%s Client] Successfully connected to %s", clientType, address)
	return esClient, nil
}

// Gets indices matching the pattern from the source
func getSourceIndices(pattern string) ([]string, error) {
	res, err := sourceClient.Cat.Indices(
		sourceClient.Cat.Indices.WithIndex(pattern),
		sourceClient.Cat.Indices.WithFormat("json"),
		sourceClient.Cat.Indices.WithH("index"),
	)
	if err != nil {
		return nil, fmt.Errorf("cannot get source indices for pattern '%s': %w", pattern, err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, fmt.Errorf("error response when getting source indices for pattern '%s': %s", pattern, res.String())
	}

	var indicesInfo []map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&indicesInfo); err != nil {
		return nil, fmt.Errorf("error decoding source indices response: %w", err)
	}

	var indices []string
	for _, info := range indicesInfo {
		if indexName, ok := info["index"].(string); ok {
			indices = append(indices, indexName)
		}
	}

	if len(indices) == 0 {
		log.Printf("Warning: No source indices found matching pattern: %s", pattern)
	} else {
		log.Printf("Found source indices matching '%s': %v", pattern, indices)
	}

	return indices, nil
}

// Processes each index matching the pattern
func processIndices(appCfg Config) {
	sourceIndices, err := getSourceIndices(appCfg.IndexName)
	if err != nil {
		log.Fatalf("Failed to get source indices: %v", err)
	}

	if len(sourceIndices) == 0 {
		log.Fatalf("No indices found matching source pattern '%s'. Aborting.", appCfg.IndexName)
	}

	// Handle TARGET_INDEX_NAME logic
	targetIndexNameOverride := appCfg.TargetIndexName
	if targetIndexNameOverride != "" && len(sourceIndices) > 1 {
		log.Fatalf("Error: TARGET_INDEX_NAME ('%s') is specified, but the source pattern '%s' matched multiple indices (%v). TARGET_INDEX_NAME can only be used when the source pattern matches exactly one index.", targetIndexNameOverride, appCfg.IndexName, sourceIndices)
	}

	for _, sourceIndexName := range sourceIndices {
		targetIndexName := sourceIndexName // Default: target name = source name
		if targetIndexNameOverride != "" && len(sourceIndices) == 1 {
			targetIndexName = targetIndexNameOverride // Override if specified and only one source index
			log.Printf("Info: Using specified target index name '%s' for source index '%s'", targetIndexName, sourceIndexName)
		}

		log.Printf("--- Starting clone: [%s] -> [%s] ---", sourceIndexName, targetIndexName)
		cloneIndex(sourceIndexName, targetIndexName, appCfg)
		log.Printf("--- Finished clone: [%s] -> [%s] ---", sourceIndexName, targetIndexName)
	}
}

// Clones a single source index to a target index name
func cloneIndex(sourceIndexName, targetIndexName string, cfg Config) {
	// 1. Get source index mapping and settings
	mapping, settings, err := getIndexMetadata(sourceClient, sourceIndexName)
	if err != nil {
		log.Printf("Error getting metadata for source index %s: %v. Skipping.", sourceIndexName, err)
		return
	}
	log.Printf("Retrieved mapping and settings for source index: %s", sourceIndexName)

	// 2. Create target index with source mapping and settings
	err = createIndexWithMetadata(targetClient, targetIndexName, mapping, settings)
	if err != nil {
		if strings.Contains(err.Error(), "resource_already_exists_exception") {
			log.Printf("Warning: Target index %s already exists. Skipping creation, will proceed to data copy.", targetIndexName)
		} else {
			log.Printf("Error creating target index %s: %v. Skipping index.", targetIndexName, err)
			return
		}
	} else {
		log.Printf("Successfully created target index: %s", targetIndexName)
	}

	// 3. Scroll and bulk index data
	err = scrollAndBulkIndex(sourceIndexName, targetIndexName, cfg)
	if err != nil {
		log.Printf("Error during data copy for index %s -> %s: %v", sourceIndexName, targetIndexName, err)
	} else {
		log.Printf("Data copy finished for index %s -> %s", sourceIndexName, targetIndexName)
	}

	// 4. Refresh target index
	err = refreshIndex(targetClient, targetIndexName)
	if err != nil {
		log.Printf("Warning: Failed to refresh target index %s: %v", targetIndexName, err)
	} else {
		log.Printf("Target index %s refreshed.", targetIndexName)
	}
}

// Gets mapping and settings for a given index
func getIndexMetadata(client *elasticsearch.Client, indexName string) (map[string]interface{}, map[string]interface{}, error) {
	res, err := client.Indices.Get(
		[]string{indexName},
		client.Indices.Get.WithContext(context.Background()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting index metadata request: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, nil, fmt.Errorf("error response getting index metadata: %s", res.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return nil, nil, fmt.Errorf("error decoding index metadata response: %w", err)
	}

	indexData, ok := response[indexName].(map[string]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("unexpected structure in index metadata response for '%s'", indexName)
	}

	mappings, _ := indexData["mappings"].(map[string]interface{})
	settingsData, _ := indexData["settings"].(map[string]interface{})

	cleanedSettings := cleanSettings(settingsData)

	return mappings, cleanedSettings, nil
}

// Cleans settings obtained from the source index
func cleanSettings(settingsData map[string]interface{}) map[string]interface{} {
	if settingsData == nil {
		return nil
	}

	indexSettings, ok := settingsData["index"].(map[string]interface{})
	if !ok {
		log.Println("Warning: Could not find 'index' key in settings, returning settings as is.")
		return settingsData
	}

	keysToRemove := []string{
		"creation_date", "uuid", "version", "provided_name", "created",
		"routing.allocation.include._tier_preference", // Often causes issues if target doesn't have same tiers
	}

	cleanedIndexSettings := make(map[string]interface{})
	for key, value := range indexSettings {
		remove := false
		for _, removeKey := range keysToRemove {
			if key == removeKey {
				remove = true
				break
			}
		}
		if key == "version" { // Remove the nested version map
			remove = true
		}
		if !remove {
			cleanedIndexSettings[key] = value
		}
	}

	return map[string]interface{}{
		"index": cleanedIndexSettings,
	}
}

// Creates an index on the target client with the given mapping and settings
func createIndexWithMetadata(client *elasticsearch.Client, targetIndexName string, mapping map[string]interface{}, settings map[string]interface{}) error {
	createBody := map[string]interface{}{}
	if mapping != nil && len(mapping) > 0 {
		createBody["mappings"] = mapping
	}
	if settings != nil {
		// Check if settings has the 'index' key after cleaning
		if indexSettings, ok := settings["index"]; ok && len(indexSettings.(map[string]interface{})) > 0 {
			createBody["settings"] = settings
		} else if !ok && len(settings) > 0 {
			// Fallback if cleaning somehow removed the 'index' key but settings remain
			log.Println("Info: Wrapping non-standard settings under 'index' key for create index request.")
			createBody["settings"] = map[string]interface{}{"index": settings}
		}
	}

	if len(createBody) == 0 {
		log.Printf("Info: No mapping or transferable settings found/provided for target index %s. Creating with defaults.", targetIndexName)
	}

	bodyBytes, err := json.Marshal(createBody)
	if err != nil {
		return fmt.Errorf("error marshaling create index request body: %w", err)
	}

	// Log cautiously - might contain sensitive mapping data? Maybe just log keys?
	// log.Printf("Creating target index %s with body: %s", targetIndexName, string(bodyBytes))
	log.Printf("Creating target index %s with mappings and settings...", targetIndexName)

	res, err := client.Indices.Create(
		targetIndexName,
		client.Indices.Create.WithContext(context.Background()),
		client.Indices.Create.WithBody(bytes.NewReader(bodyBytes)),
	)

	if err != nil {
		return fmt.Errorf("error in create index request for %s: %w", targetIndexName, err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return fmt.Errorf("error response creating index %s: %s", targetIndexName, res.String())
	}

	var createResponse map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&createResponse); err != nil {
		log.Printf("Warning: Could not decode create index response for %s: %v", targetIndexName, err)
	} else {
		acknowledged, _ := createResponse["acknowledged"].(bool)
		if !acknowledged {
			log.Printf("Warning: Create index request for %s was not acknowledged by all nodes.", targetIndexName)
		}
	}

	return nil
}

// Scrolls through source index and bulk indexes data into the target index name
func scrollAndBulkIndex(sourceIndexName, targetIndexName string, cfg Config) error {
	ctx := context.Background()
	var wg sync.WaitGroup
	// Buffer size slightly larger to prevent blocking scroll while workers process
	docChan := make(chan json.RawMessage, cfg.BatchSize*(cfg.Workers+1))

	log.Printf("Starting scroll for source index %s with batch size %d", sourceIndexName, cfg.BatchSize)

	res, err := sourceClient.Search(
		sourceClient.Search.WithIndex(sourceIndexName),
		sourceClient.Search.WithSize(cfg.BatchSize),
		sourceClient.Search.WithScroll(cfg.ScrollDuration),
		sourceClient.Search.WithContext(ctx),
		sourceClient.Search.WithBody(strings.NewReader(`{"query": {"match_all": {}}}`)),
		sourceClient.Search.WithSort("_doc"),
	)
	if err != nil {
		return fmt.Errorf("initial scroll request failed for %s: %w", sourceIndexName, err)
	}

	var scrollID string
	var totalHits int64
	var docsInBatch int

	scrollID, totalHits, docsInBatch, err = processScrollResponse(res, docChan)
	if err != nil {
		return fmt.Errorf("processing initial scroll batch failed for %s: %w", sourceIndexName, err)
	}
	if totalHits == 0 {
		log.Printf("Info: Source index %s contains no documents to clone.", sourceIndexName)
		close(docChan)
		clearScroll(sourceClient, scrollID) // Clear scroll even if no hits
		return nil
	}

	log.Printf("Total documents to process for index %s: %d", sourceIndexName, totalHits)
	processedCount := int64(docsInBatch)

	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go bulkWorker(i, targetIndexName, docChan, &wg, cfg.BatchSize)
	}

	for scrollID != "" {
		log.Printf("Processed approx %d / %d documents for %s...", processedCount, totalHits, sourceIndexName)
		res, err := sourceClient.Scroll(
			sourceClient.Scroll.WithScrollID(scrollID),
			sourceClient.Scroll.WithScroll(cfg.ScrollDuration),
			sourceClient.Scroll.WithContext(ctx),
		)
		if err != nil {
			clearScroll(sourceClient, scrollID)
			return fmt.Errorf("subsequent scroll request failed for %s: %w", sourceIndexName, err)
		}

		scrollID, _, docsInBatch, err = processScrollResponse(res, docChan)
		if err != nil {
			clearScroll(sourceClient, scrollID)
			return fmt.Errorf("processing subsequent scroll batch failed for %s: %w", sourceIndexName, err)
		}

		processedCount += int64(docsInBatch)

		if docsInBatch == 0 { // End of scroll implicitly
			log.Printf("Scroll finished for %s (empty batch received).", sourceIndexName)
			break
		}
	}

	if scrollID != "" {
		clearScroll(sourceClient, scrollID)
	}

	log.Println("Finished scrolling, closing document channel for", sourceIndexName)
	close(docChan)

	log.Println("Waiting for bulk workers to finish for index", sourceIndexName)
	wg.Wait()

	log.Println("All bulk workers finished for index", sourceIndexName)
	return nil
}

// Processes a scroll response, sends documents to the channel, returns scroll ID, total hits, docs in this batch
func processScrollResponse(res *esapi.Response, docChan chan<- json.RawMessage) (string, int64, int, error) {
	defer res.Body.Close()
	if res.IsError() {
		return "", 0, 0, fmt.Errorf("scroll response error: %s", res.String())
	}

	var r map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return "", 0, 0, fmt.Errorf("error decoding scroll response: %w", err)
	}

	newScrollID, _ := r["_scroll_id"].(string)
	docsInThisBatch := 0
	var totalHits int64 // Only populated meaningfully on first call

	if hitsData, ok := r["hits"].(map[string]interface{}); ok {
		if total, ok := hitsData["total"].(map[string]interface{}); ok {
			if value, ok := total["value"].(float64); ok {
				totalHits = int64(value)
			}
		}
		hits, ok := hitsData["hits"].([]interface{})
		if ok {
			docsInThisBatch = len(hits)
			for _, hit := range hits {
				hitMap, ok := hit.(map[string]interface{})
				if !ok {
					continue
				}

				doc := map[string]interface{}{
					"_index":  hitMap["_index"], // Keep original source index here for potential debugging
					"_id":     hitMap["_id"],
					"_source": hitMap["_source"],
				}
				docBytes, err := json.Marshal(doc)
				if err != nil {
					continue
				}
				docChan <- json.RawMessage(docBytes)
			}
		}
	}

	// If scroll ID is missing/empty in response, means scroll is finished
	if newScrollID == "" && docsInThisBatch > 0 {
		// This case shouldn't normally happen if ES behaves, but good to know
		log.Println("Warning: Received non-empty batch but no scroll ID in response. Assuming scroll ended.")
	}

	return newScrollID, totalHits, docsInThisBatch, nil
}

// Worker goroutine for bulk indexing to the specified target index
func bulkWorker(id int, targetIndexName string, docChan <-chan json.RawMessage, wg *sync.WaitGroup, batchSize int) {
	defer wg.Done()
	log.Printf("[Worker %d] Starting for target index %s", id, targetIndexName)

	var buffer bytes.Buffer
	var docsInBatch int

	for docBytes := range docChan {
		meta := map[string]interface{}{
			"index": map[string]interface{}{
				"_index": targetIndexName, // Use the target index name here
				"_id":    extractID(docBytes),
			},
		}
		metaBytes, _ := json.Marshal(meta)

		buffer.Write(metaBytes)
		buffer.WriteByte('\n')

		sourceBytes := extractSource(docBytes)
		buffer.Write(sourceBytes)
		buffer.WriteByte('\n')

		docsInBatch++

		if docsInBatch >= batchSize {
			sendBulkRequest(id, targetIndexName, &buffer)
			buffer.Reset()
			docsInBatch = 0
		}
	}

	if buffer.Len() > 0 {
		log.Printf("[Worker %d] Sending final batch of %d docs to %s", id, docsInBatch, targetIndexName)
		sendBulkRequest(id, targetIndexName, &buffer)
	}

	log.Printf("[Worker %d] Finished for target index %s", id, targetIndexName)
}

// Helper to extract _id
func extractID(docBytes json.RawMessage) string {
	var doc map[string]interface{}
	if err := json.Unmarshal(docBytes, &doc); err == nil {
		if id, ok := doc["_id"].(string); ok {
			return id
		}
	}
	return ""
}

// Helper to extract _source
func extractSource(docBytes json.RawMessage) json.RawMessage {
	var doc map[string]interface{}
	if err := json.Unmarshal(docBytes, &doc); err == nil {
		if source, ok := doc["_source"]; ok {
			sourceBytes, err := json.Marshal(source)
			if err == nil {
				return json.RawMessage(sourceBytes)
			}
		}
	}
	log.Println("Warning: Could not extract _source from document")
	return json.RawMessage("{}")
}

// Sends a bulk request to the target cluster for a specific index
func sendBulkRequest(workerID int, targetIndexName string, buffer *bytes.Buffer) {
	if buffer.Len() == 0 {
		return
	}

	res, err := targetClient.Bulk(
		bytes.NewReader(buffer.Bytes()),
		targetClient.Bulk.WithContext(context.Background()),
		// Optional: Can specify target index here too, but action metadata should handle it
		// targetClient.Bulk.WithIndex(targetIndexName),
	)

	if err != nil {
		log.Printf("[Worker %d] Error sending bulk request to %s: %v", workerID, targetIndexName, err)
		return
	}
	defer res.Body.Close()

	if res.IsError() {
		log.Printf("[Worker %d] Error response from bulk request to %s: %s", workerID, targetIndexName, res.String())
	} else {
		var bulkResponse map[string]interface{}
		if err := json.NewDecoder(res.Body).Decode(&bulkResponse); err == nil {
			if errors, ok := bulkResponse["errors"].(bool); ok && errors {
				log.Printf("[Worker %d] Bulk request to %s completed with item-level errors.", workerID, targetIndexName)
			}
		} else {
			log.Printf("[Worker %d] Warning: Could not decode bulk response for %s: %v", workerID, targetIndexName, err)
		}
	}
}

// Clears a scroll context on the source cluster
func clearScroll(client *elasticsearch.Client, scrollID string) {
	if scrollID == "" {
		return
	}
	// log.Printf("Clearing scroll ID: %s", scrollID) // Can be noisy
	res, err := client.ClearScroll(
		client.ClearScroll.WithScrollID(scrollID),
		client.ClearScroll.WithContext(context.Background()),
	)
	if err != nil {
		log.Printf("Warning: Failed to clear scroll context %s: %v", scrollID, err)
	} else if res != nil {
		defer res.Body.Close()
		if res.IsError() {
			// log.Printf("Warning: Error response clearing scroll context %s: %s", scrollID, res.String())
		}
	}
}

// Refreshes the target index to make changes visible
func refreshIndex(client *elasticsearch.Client, targetIndexName string) error {
	log.Printf("Refreshing target index: %s", targetIndexName)
	res, err := client.Indices.Refresh(
		client.Indices.Refresh.WithIndex(targetIndexName),
		client.Indices.Refresh.WithContext(context.Background()),
	)
	if err != nil {
		return fmt.Errorf("refresh request failed for index %s: %w", targetIndexName, err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("error response refreshing index %s: %s", targetIndexName, res.String())
	}
	return nil
}
