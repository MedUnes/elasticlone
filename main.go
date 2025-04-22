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

// Configuration struct (optional but good practice for clarity)
type Config struct {
	SourceHost     string
	SourcePort     int
	SourceUser     string
	SourcePass     string
	TargetHost     string
	TargetPort     int
	TargetUser     string
	TargetPass     string
	IndexName      string
	BatchSize      int
	Workers        int
	SSLVerify      bool
	ScrollDuration time.Duration
	MaxRetries     int
}

// Helper function to get environment variables with defaults and logging
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	if fallback != "" {
		log.Printf("Warning: Environment variable %s not set, using default: %s", key, fallback)
	} else {
		log.Printf("Warning: Environment variable %s not set, no default provided", key)
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
	// Load .env file. It's okay if it fails, environment variables might be set directly.
	err := godotenv.Load()
	if err != nil {
		log.Println("Info: No .env file found or error loading it. Relying on existing environment variables.")
	}

	// --- Load Configuration ---
	cfg := Config{}

	// Source Config
	cfg.SourceHost = getRequiredEnv("SOURCE_HOST")
	sourcePortStr := getRequiredEnv("SOURCE_PORT")
	cfg.SourceUser = os.Getenv("SOURCE_USER") // Optional
	cfg.SourcePass = os.Getenv("SOURCE_PASS") // Optional

	// Target Config
	cfg.TargetHost = getRequiredEnv("TARGET_HOST")
	targetPortStr := getRequiredEnv("TARGET_PORT")
	cfg.TargetUser = os.Getenv("TARGET_USER") // Optional
	cfg.TargetPass = os.Getenv("TARGET_PASS") // Optional

	// Cloning Config
	cfg.IndexName = getRequiredEnv("INDEX_NAME")
	batchSizeStr := getEnv("BATCH_SIZE", "1000")
	workersStr := getEnv("WORKERS", "4")
	sslVerifyStr := getEnv("SSL_VERIFY", "true")
	scrollDurationStr := getEnv("SCROLL_DURATION", "1m") // Default scroll duration
	maxRetriesStr := getEnv("MAX_RETRIES", "3")          // Default max retries

	// Parse numeric and boolean values with error handling and defaults
	var parseErr error
	cfg.SourcePort, parseErr = strconv.Atoi(sourcePortStr)
	if parseErr != nil {
		log.Fatalf("Error: Invalid SOURCE_PORT value '%s'. Must be an integer.", sourcePortStr)
	}
	cfg.TargetPort, parseErr = strconv.Atoi(targetPortStr)
	if parseErr != nil {
		log.Fatalf("Error: Invalid TARGET_PORT value '%s'. Must be an integer.", targetPortStr)
	}
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
	sourceClient, err = createEsClient(cfg.SourceHost, cfg.SourcePort, cfg.SourceUser, cfg.SourcePass, cfg.SSLVerify, cfg.MaxRetries)
	if err != nil {
		log.Fatalf("Error creating source Elasticsearch client: %v", err)
	}
	targetClient, err = createEsClient(cfg.TargetHost, cfg.TargetPort, cfg.TargetUser, cfg.TargetPass, cfg.SSLVerify, cfg.MaxRetries)
	if err != nil {
		log.Fatalf("Error creating target Elasticsearch client: %v", err)
	}

	log.Println("Source and target Elasticsearch clients initialized.")
	log.Printf("Attempting to clone index pattern: %s", cfg.IndexName)
	log.Printf("Batch size: %d, Workers: %d", cfg.BatchSize, cfg.Workers)

	// --- Start Cloning Process ---
	processIndices(cfg)

	log.Println("Cloning process finished.")
}

// Creates an Elasticsearch client
func createEsClient(host string, port int, user, pass string, sslVerify bool, maxRetries int) (*elasticsearch.Client, error) {
	cfg := elasticsearch.Config{
		Addresses:     []string{fmt.Sprintf("http://%s:%d", host, port)}, // Start with http, adjust below
		Username:      user,
		Password:      pass,
		RetryOnStatus: []int{502, 503, 504, 429}, // Add 429 Too Many Requests
		MaxRetries:    maxRetries,
		RetryBackoff: func(i int) time.Duration { // Exponential backoff
			return time.Duration(1<<uint(i)) * time.Second
		},
		Transport: &http.Transport{
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: time.Second * 30, // Increased timeout
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: !sslVerify, // Use the config value
			},
		},
	}

	// Determine scheme based on port or SSL verification needs
	// Basic heuristic: use https if port is 443 or if SSL verification is explicitly enabled (even on other ports)
	// or if username/password are provided (usually implies HTTPS needed)
	if port == 443 || sslVerify || (user != "" && pass != "") {
		cfg.Addresses = []string{fmt.Sprintf("https://%s:%d", host, port)}
		log.Printf("Info: Using HTTPS for %s:%d", host, port)
	} else {
		log.Printf("Info: Using HTTP for %s:%d. Enable SSL_VERIFY=true or use port 443 for HTTPS.", host, port)
	}

	esClient, err := elasticsearch.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("error creating client: %w", err)
	}

	// Test connection
	res, err := esClient.Info()
	if err != nil {
		return nil, fmt.Errorf("cannot connect to %s:%d: %w", host, port, err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, fmt.Errorf("connection error to %s:%d: %s", host, port, res.String())
	}
	log.Printf("Successfully connected to Elasticsearch node: %s:%d", host, port)
	return esClient, nil
}

// Gets indices matching the pattern from the source
func getSourceIndices(pattern string) ([]string, error) {
	res, err := sourceClient.Cat.Indices(
		sourceClient.Cat.Indices.WithIndex(pattern),
		sourceClient.Cat.Indices.WithFormat("json"),
		sourceClient.Cat.Indices.WithH("index"), // Only get index names
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
	indices, err := getSourceIndices(appCfg.IndexName)
	if err != nil {
		log.Fatalf("Failed to get source indices: %v", err)
	}

	if len(indices) == 0 {
		log.Fatalf("No indices found matching pattern '%s' on source cluster. Aborting.", appCfg.IndexName)
	}

	for _, indexName := range indices {
		log.Printf("--- Starting clone for index: %s ---", indexName)
		cloneIndex(indexName, appCfg)
		log.Printf("--- Finished clone for index: %s ---", indexName)
	}
}

// Clones a single index from source to target
func cloneIndex(indexName string, cfg Config) {
	// 1. Get source index mapping and settings
	mapping, settings, err := getIndexMetadata(sourceClient, indexName)
	if err != nil {
		log.Printf("Error getting metadata for source index %s: %v. Skipping.", indexName, err)
		return
	}
	log.Printf("Retrieved mapping and settings for source index: %s", indexName)

	// 2. Create target index with source mapping and settings
	err = createIndexWithMetadata(targetClient, indexName, mapping, settings)
	if err != nil {
		// Check if the error is because the index already exists
		if strings.Contains(err.Error(), "resource_already_exists_exception") {
			log.Printf("Warning: Target index %s already exists. Skipping creation, will proceed to data copy.", indexName)
		} else {
			log.Printf("Error creating target index %s: %v. Skipping index.", indexName, err)
			return
		}
	} else {
		log.Printf("Successfully created target index: %s", indexName)
	}

	// 3. Scroll and bulk index data
	err = scrollAndBulkIndex(indexName, cfg)
	if err != nil {
		log.Printf("Error during data copy for index %s: %v", indexName, err)
	} else {
		log.Printf("Data copy finished for index %s", indexName)
	}

	// 4. Refresh target index
	err = refreshIndex(targetClient, indexName)
	if err != nil {
		log.Printf("Warning: Failed to refresh target index %s: %v", indexName, err)
	} else {
		log.Printf("Target index %s refreshed.", indexName)
	}
}

// Gets mapping and settings for a given index
func getIndexMetadata(client *elasticsearch.Client, indexName string) (map[string]interface{}, map[string]interface{}, error) {
	res, err := client.Indices.Get(
		[]string{indexName},
		client.Indices.Get.WithContext(context.Background()),
		// client.Indices.Get.WithIncludeDefaults(true), // Might include too much? Test if needed.
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

	// Clean settings: remove index-specific, non-transferable settings
	cleanedSettings := cleanSettings(settingsData)

	return mappings, cleanedSettings, nil
}

// Cleans settings obtained from the source index to make them suitable for creating a new index
func cleanSettings(settingsData map[string]interface{}) map[string]interface{} {
	if settingsData == nil {
		return nil
	}

	indexSettings, ok := settingsData["index"].(map[string]interface{})
	if !ok {
		log.Println("Warning: Could not find 'index' key in settings, returning settings as is.")
		return settingsData // Return original if structure is unexpected
	}

	// Settings to remove - these are assigned at creation or managed by Elasticsearch
	keysToRemove := []string{
		"creation_date",
		"uuid",
		"version",
		"provided_name",
		"created", // Often nested under version
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
		// Also remove the 'version' map entirely if it exists
		if key == "version" {
			remove = true
		}

		if !remove {
			cleanedIndexSettings[key] = value
		}
	}

	// Return settings structure compatible with create index API (needs the "index" wrapper)
	return map[string]interface{}{
		"index": cleanedIndexSettings,
	}
}

// Creates an index on the target client with the given mapping and settings
func createIndexWithMetadata(client *elasticsearch.Client, indexName string, mapping map[string]interface{}, settings map[string]interface{}) error {
	createBody := map[string]interface{}{}
	if mapping != nil && len(mapping) > 0 {
		createBody["mappings"] = mapping
	}
	if settings != nil && len(settings) > 0 {
		// Use the potentially cleaned settings directly (which should already have the "index" key if cleaning worked)
		// Or, if cleaning failed or wasn't needed, ensure the structure is correct.
		if _, ok := settings["index"]; ok {
			createBody["settings"] = settings
		} else if len(settings) > 0 {
			// If 'index' key is missing, wrap the settings - this might happen if cleanSettings bypassed cleaning
			log.Println("Info: Wrapping settings under 'index' key for create index request.")
			createBody["settings"] = map[string]interface{}{"index": settings}
		}
	}

	// Only proceed if there's actually something to create (mapping or settings)
	if len(createBody) == 0 {
		log.Printf("Info: No mapping or settings provided for index %s. Creating with defaults.", indexName)
		// Allow ES to create with defaults if neither mapping nor settings are present
	}

	bodyBytes, err := json.Marshal(createBody)
	if err != nil {
		return fmt.Errorf("error marshaling create index request body: %w", err)
	}

	log.Printf("Creating target index %s with body: %s", indexName, string(bodyBytes))

	res, err := client.Indices.Create(
		indexName,
		client.Indices.Create.WithContext(context.Background()),
		client.Indices.Create.WithBody(bytes.NewReader(bodyBytes)),
	)

	if err != nil {
		return fmt.Errorf("error in create index request for %s: %w", indexName, err)
	}
	defer res.Body.Close()

	// Check specifically for acknowledgment, but handle errors generally
	if res.IsError() {
		return fmt.Errorf("error response creating index %s: %s", indexName, res.String())
	}

	// Decode response to check for acknowledgment (optional but good)
	var createResponse map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&createResponse); err != nil {
		log.Printf("Warning: Could not decode create index response for %s: %v", indexName, err)
		// Don't fail here, the request might have succeeded without ack being parsed
	} else {
		acknowledged, _ := createResponse["acknowledged"].(bool)
		if !acknowledged {
			log.Printf("Warning: Create index request for %s was not acknowledged by all nodes.", indexName)
		}
	}

	return nil
}

// Scrolls through source index and bulk indexes data into the target
func scrollAndBulkIndex(indexName string, cfg Config) error {
	ctx := context.Background()
	var wg sync.WaitGroup
	docChan := make(chan json.RawMessage, cfg.BatchSize*cfg.Workers) // Buffered channel

	log.Printf("Starting scroll for index %s with batch size %d", indexName, cfg.BatchSize)

	// Start initial scroll request
	res, err := sourceClient.Search(
		sourceClient.Search.WithIndex(indexName),
		sourceClient.Search.WithSize(cfg.BatchSize),
		sourceClient.Search.WithScroll(cfg.ScrollDuration),
		sourceClient.Search.WithContext(ctx),
		sourceClient.Search.WithBody(strings.NewReader(`{"query": {"match_all": {}}}`)), // Fetch all documents
		sourceClient.Search.WithSort("_doc"),                                            // Use efficient _doc sort order
	)
	if err != nil {
		return fmt.Errorf("initial scroll request failed: %w", err)
	}

	var scrollID string
	var totalHits int64

	// Process initial batch
	scrollID, totalHits, err = processScrollResponse(res, docChan)
	if err != nil {
		return fmt.Errorf("processing initial scroll batch failed: %w", err)
	}
	if totalHits == 0 {
		log.Printf("Info: Index %s contains no documents to clone.", indexName)
		close(docChan) // Ensure channel is closed even if no docs
		return nil     // Nothing more to do
	}

	log.Printf("Total documents to process for index %s: %d", indexName, totalHits)
	processedCount := int64(len(docChan)) // Count docs from initial batch already in channel

	// Start worker goroutines for bulk indexing
	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go bulkWorker(i, indexName, docChan, &wg, cfg.BatchSize)
	}

	// Continue scrolling
	for scrollID != "" {
		log.Printf("Processed %d / %d documents...", processedCount, totalHits)
		res, err := sourceClient.Scroll(
			sourceClient.Scroll.WithScrollID(scrollID),
			sourceClient.Scroll.WithScroll(cfg.ScrollDuration),
			sourceClient.Scroll.WithContext(ctx),
		)
		if err != nil {
			// Attempt to clear scroll before returning error
			clearScroll(sourceClient, scrollID)
			return fmt.Errorf("subsequent scroll request failed: %w", err)
		}

		var currentBatchSize int
		scrollID, _, err = processScrollResponse(res, docChan) // Total hits isn't needed after first batch
		if err != nil {
			clearScroll(sourceClient, scrollID) // Attempt to clear scroll even on processing error
			return fmt.Errorf("processing subsequent scroll batch failed: %w", err)
		}

		// Estimate processed count - relies on channel buffer size knowledge
		// A more accurate count would require atomic counters updated by workers
		processedCount += int64(currentBatchSize) // Add size of the batch just processed

		// If scrollID becomes empty, it means we've reached the end
		if scrollID == "" {
			break
		}
	}

	// Attempt to clear the last scroll ID
	if scrollID != "" {
		clearScroll(sourceClient, scrollID)
	}

	log.Println("Finished scrolling, closing document channel.")
	close(docChan) // Signal workers that no more documents are coming

	log.Println("Waiting for bulk workers to finish...")
	wg.Wait() // Wait for all workers to complete processing remaining docs

	log.Println("All bulk workers finished.")
	return nil
}

// Processes a scroll response, sends documents to the channel, returns scroll ID and total hits
func processScrollResponse(res *esapi.Response, docChan chan<- json.RawMessage) (string, int64, error) {
	defer res.Body.Close()
	if res.IsError() {
		return "", 0, fmt.Errorf("scroll response error: %s", res.String())
	}

	var r map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return "", 0, fmt.Errorf("error decoding scroll response: %w", err)
	}

	newScrollID, _ := r["_scroll_id"].(string)

	// Extract total hits only from the initial response if possible
	var totalHits int64
	if hitsData, ok := r["hits"].(map[string]interface{}); ok {
		if total, ok := hitsData["total"].(map[string]interface{}); ok {
			if value, ok := total["value"].(float64); ok { // JSON numbers are float64
				totalHits = int64(value)
			}
		}
		// Extract documents
		hits, ok := hitsData["hits"].([]interface{})
		if ok {
			for _, hit := range hits {
				hitMap, ok := hit.(map[string]interface{})
				if !ok {
					log.Println("Warning: Could not parse hit structure")
					continue
				}

				// Prepare document for bulk index: needs _index, _id, _source
				doc := map[string]interface{}{
					"_index":  hitMap["_index"],
					"_id":     hitMap["_id"],
					"_source": hitMap["_source"],
				}

				docBytes, err := json.Marshal(doc)
				if err != nil {
					log.Printf("Warning: Failed to marshal document %s: %v", hitMap["_id"], err)
					continue
				}
				docChan <- json.RawMessage(docBytes)
			}
			return newScrollID, totalHits, nil // Return after processing hits
		}
	}

	// Return scroll ID even if hits processing failed or no hits found in this batch
	return newScrollID, 0, nil // Return 0 total hits if not found in this response
}

// Worker goroutine for bulk indexing
func bulkWorker(id int, indexName string, docChan <-chan json.RawMessage, wg *sync.WaitGroup, batchSize int) {
	defer wg.Done()
	log.Printf("[Worker %d] Starting", id)

	var buffer bytes.Buffer
	var docsInBatch int

	for docBytes := range docChan {
		// Each bulk request item needs two lines: action metadata + document source
		meta := map[string]interface{}{
			"index": map[string]interface{}{
				"_index": indexName,
				// Extract _id from the docBytes (which contains _index, _id, _source)
				"_id": extractID(docBytes),
			},
		}
		metaBytes, _ := json.Marshal(meta) // Error handling omitted for brevity

		// Append action metadata line
		buffer.Write(metaBytes)
		buffer.WriteByte('\n')

		// Append document source line (_source only)
		sourceBytes := extractSource(docBytes)
		buffer.Write(sourceBytes)
		buffer.WriteByte('\n')

		docsInBatch++

		if docsInBatch >= batchSize {
			sendBulkRequest(id, &buffer)
			// Reset buffer and counter after sending
			buffer.Reset()
			docsInBatch = 0
		}
	}

	// Send any remaining documents in the buffer after the channel closes
	if buffer.Len() > 0 {
		log.Printf("[Worker %d] Sending final batch of %d docs", id, docsInBatch)
		sendBulkRequest(id, &buffer)
	}

	log.Printf("[Worker %d] Finished", id)
}

// Helper to extract _id (assumes docBytes has {"_id": "...", ...})
func extractID(docBytes json.RawMessage) string {
	var doc map[string]interface{}
	if err := json.Unmarshal(docBytes, &doc); err == nil {
		if id, ok := doc["_id"].(string); ok {
			return id
		}
	}
	return "" // Should not happen if processScrollResponse worked correctly
}

// Helper to extract _source (assumes docBytes has {"_source": {...}, ...})
func extractSource(docBytes json.RawMessage) json.RawMessage {
	var doc map[string]interface{}
	if err := json.Unmarshal(docBytes, &doc); err == nil {
		if source, ok := doc["_source"]; ok {
			// Re-marshal just the source part
			sourceBytes, err := json.Marshal(source)
			if err == nil {
				return json.RawMessage(sourceBytes)
			}
		}
	}
	log.Println("Warning: Could not extract _source from document")
	return json.RawMessage("{}") // Return empty object if extraction fails
}

// Sends a bulk request to the target cluster
func sendBulkRequest(workerID int, buffer *bytes.Buffer) {
	if buffer.Len() == 0 {
		return
	}

	res, err := targetClient.Bulk(
		bytes.NewReader(buffer.Bytes()),
		targetClient.Bulk.WithContext(context.Background()),
	)

	if err != nil {
		log.Printf("[Worker %d] Error sending bulk request: %v", workerID, err)
		// Consider adding retry logic here
		return
	}
	defer res.Body.Close()

	if res.IsError() {
		log.Printf("[Worker %d] Error response from bulk request: %s", workerID, res.String())
		// Consider logging the failing bulk payload (buffer.String()) for debugging
		// but be mindful of sensitive data and log size.
	} else {
		// Optional: Check response body for item-level errors
		var bulkResponse map[string]interface{}
		if err := json.NewDecoder(res.Body).Decode(&bulkResponse); err == nil {
			if errors, ok := bulkResponse["errors"].(bool); ok && errors {
				log.Printf("[Worker %d] Bulk request completed with item-level errors. See Elasticsearch logs for details.", workerID)
				// Optionally log specific errors if needed, but can be verbose
				// logFailedBulkItems(bulkResponse)
			} else {
				// log.Printf("[Worker %d] Bulk request successful.", workerID) // Can be too noisy
			}
		} else {
			log.Printf("[Worker %d] Warning: Could not decode bulk response: %v", workerID, err)
		}
	}

}

// Clears a scroll context on the source cluster
func clearScroll(client *elasticsearch.Client, scrollID string) {
	if scrollID == "" {
		return
	}
	log.Printf("Clearing scroll ID: %s", scrollID)
	res, err := client.ClearScroll(
		client.ClearScroll.WithScrollID(scrollID),
		client.ClearScroll.WithContext(context.Background()),
	)
	if err != nil {
		log.Printf("Warning: Failed to clear scroll context %s: %v", scrollID, err)
	} else {
		defer res.Body.Close()
		if res.IsError() {
			log.Printf("Warning: Error response clearing scroll context %s: %s", scrollID, res.String())
		} else {
			log.Printf("Successfully cleared scroll ID: %s", scrollID)
		}
	}
}

// Refreshes the target index to make changes visible
func refreshIndex(client *elasticsearch.Client, indexName string) error {
	log.Printf("Refreshing target index: %s", indexName)
	res, err := client.Indices.Refresh(
		client.Indices.Refresh.WithIndex(indexName),
		client.Indices.Refresh.WithContext(context.Background()),
	)
	if err != nil {
		return fmt.Errorf("refresh request failed for index %s: %w", indexName, err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("error response refreshing index %s: %s", indexName, res.String())
	}
	return nil
}

func logFailedBulkItems(bulkResponse map[string]interface{}) {
	items, ok := bulkResponse["items"].([]interface{})
	if !ok {
		return
	}
	for _, item := range items {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		for _, action := range []string{"index", "create", "update", "delete"} {
			if actionResult, ok := itemMap[action].(map[string]interface{}); ok {
				if errorInfo, exists := actionResult["error"]; exists {
					log.Printf("  - Bulk item failed: Action=%s, ID=%v, Status=%v, Error=%v",
						action,
						actionResult["_id"],
						actionResult["status"],
						errorInfo)
				}
				break
			}
		}
	}
}
