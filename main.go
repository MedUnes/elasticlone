package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Index struct {
	Settings map[string]interface{} `json:"settings"`
	Mappings map[string]interface{} `json:"mappings"`
}

type ReindexTask struct {
	Task string `json:"task"`
}

type TaskStatus struct {
	Completed bool `json:"completed"`
}

func loadConfig(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	config := make(map[string]string)
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid line: %s", line)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		config[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return config, nil
}

func parseBoolConfig(config map[string]string, key string, defaultValue bool) (bool, error) {
	if val, ok := config[key]; ok {
		result, err := strconv.ParseBool(val)
		if err != nil {
			return defaultValue, fmt.Errorf("invalid value for %s: %v", key, err)
		}
		return result, nil
	}
	return defaultValue, nil
}

func main() {
	config, err := loadConfig("auth.conf")
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	requiredKeys := []string{"SOURCE_URL", "SOURCE_USER", "SOURCE_PASS", "DEST_URL", "DEST_USER", "DEST_PASS"}
	for _, key := range requiredKeys {
		if _, ok := config[key]; !ok {
			log.Fatalf("missing required configuration key: %s", key)
		}
	}

	localPort := "9200"
	if lp, ok := config["LOCAL_PORT"]; ok {
		localPort = lp
	}

	copyMappings, _ := parseBoolConfig(config, "COPY_MAPPINGS", true)
	copyData, _ := parseBoolConfig(config, "COPY_DATA", true)
	copyTasks, _ := parseBoolConfig(config, "COPY_TASKS", false)
	copyPipelines, _ := parseBoolConfig(config, "COPY_PIPELINES", false)
	insecure, _ := parseBoolConfig(config, "INSECURE", false)
	debug, _ := parseBoolConfig(config, "DEBUG", false)

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: insecure,
			},
		},
	}

	target, _ := url.Parse(config["DEST_URL"])
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = httpClient.Transport

	destAuth := &url.UserPassword{
		Username: config["DEST_USER"],
		Password: config["DEST_PASS"],
	}
	target.User = destAuth

	sourceAuth := &url.UserPassword{
		Username: config["SOURCE_USER"],
		Password: config["SOURCE_PASS"],
	}
	sourceURL, _ := url.Parse(config["SOURCE_URL"])
	sourceURL.User = sourceAuth

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if debug {
			log.Printf("Proxying request to: %s", r.URL.Path)
		}
		proxy.ServeHTTP(w, r)
	})

	http.HandleFunc("/clone-indices", func(w http.ResponseWriter, r *http.Request) {
		if debug {
			log.Println("Starting index cloning process")
		}

		resp, err := httpClient.Get(sourceURL.String() + "/_cat/indices?format=json")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		var indices []map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&indices); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		for _, indexInfo := range indices {
			indexName := indexInfo["index"].(string)
			if strings.HasPrefix(indexName, ".") && !copyTasks {
				continue
			}

			if debug {
				log.Printf("Processing index: %s", indexName)
			}

			if copyMappings {
				resp, err := httpClient.Get(fmt.Sprintf("%s/%s", sourceURL.String(), indexName))
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				defer resp.Body.Close()

				var index Index
				if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}

				reqBody, _ := json.Marshal(index)
				req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/%s", config["DEST_URL"], indexName), strings.NewReader(string(reqBody)))
				req.SetBasicAuth(config["DEST_USER"], config["DEST_PASS"])
				req.Header.Add("Content-Type", "application/json")

				resp, err = httpClient.Do(req)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				resp.Body.Close()
			}

			if copyData {
				if debug {
					log.Printf("Copying data for index: %s", indexName)
				}

				reindexBody := fmt.Sprintf(`{
					"source": {
						"remote": {
							"host": "%s",
							"username": "%s",
							"password": "%s"
						},
						"index": "%s"
					},
					"dest": {
						"index": "%s"
					}
				}`, sourceURL.String(), config["SOURCE_USER"], config["SOURCE_PASS"], indexName, indexName)

				req, _ := http.NewRequest("POST", config["DEST_URL"]+"/_reindex?wait_for_completion=false", strings.NewReader(reindexBody))
				req.SetBasicAuth(config["DEST_USER"], config["DEST_PASS"])
				req.Header.Add("Content-Type", "application/json")

				resp, err := httpClient.Do(req)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}

				var task ReindexTask
				json.NewDecoder(resp.Body).Decode(&task)
				resp.Body.Close()

				for {
					time.Sleep(5 * time.Second)
					taskResp, err := httpClient.Get(fmt.Sprintf("%s/_tasks/%s", config["DEST_URL"], task.Task))
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}

					var status TaskStatus
					json.NewDecoder(taskResp.Body).Decode(&status)
					taskResp.Body.Close()

					if status.Completed {
						break
					}
				}
			}
		}

		if copyPipelines {
			if debug {
				log.Println("Copying ingest pipelines")
			}

			resp, err := httpClient.Get(sourceURL.String() + "/_ingest/pipeline")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer resp.Body.Close()

			var pipelines map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&pipelines); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			for name, pipeline := range pipelines {
				body, _ := json.Marshal(pipeline)
				req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/_ingest/pipeline/%s", config["DEST_URL"], name), strings.NewReader(string(body)))
				req.SetBasicAuth(config["DEST_USER"], config["DEST_PASS"])
				req.Header.Add("Content-Type", "application/json")

				resp, err := httpClient.Do(req)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}

		fmt.Fprintf(w, "Cloning process completed!\n")
	})

	log.Printf("Server running on port %s", localPort)
	log.Fatal(http.ListenAndServe(":"+localPort, nil))
}
