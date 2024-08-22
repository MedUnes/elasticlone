package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/olivere/elastic/v7"
)

const (
	batchSize  = 10000
	retryCount = 3
	retryDelay = 5 * time.Second
)

type ElasticConfig struct {
	URL   string
	Index string
	User  string
	Pass  string
}

func main() {
	srcUser := flag.String("U", "", "Source username")
	srcPass := flag.String("P", "", "Source password")
	srcHost := flag.String("H", "", "Source host")
	srcPort := flag.String("R", "", "Source port")
	srcSSL := flag.Bool("S", false, "Use SSL/HTTPS for source")
	srcIndex := flag.String("I", "", "Source index name")
	srcInsecure := flag.Bool("insecure", false, "Skip SSL certificate verification for source")
	fromDoc := flag.Int("F", 1, "Start copying from this document number (1-indexed)")
	toDoc := flag.Int("T", 0, "Stop copying at this document number (0 for no limit)")

	destUser := flag.String("u", "", "Target username")
	destPass := flag.String("p", "", "Target password")
	destHost := flag.String("h", "", "Target host")
	destPort := flag.String("r", "", "Target port")
	destSSL := flag.Bool("s", false, "Use SSL/HTTPS for target")
	destIndex := flag.String("i", "", "Target index name")

	flag.Parse()

	if *srcHost == "" || *destHost == "" || *srcIndex == "" || *destIndex == "" {
		fmt.Println("Missing required parameters. Please provide host and index information for both source and destination.")
		flag.Usage()
		return
	}

	srcScheme := "http"
	if *srcSSL {
		srcScheme = "https"
	}
	destScheme := "http"
	if *destSSL {
		destScheme = "https"
	}

	srcURL := fmt.Sprintf("%s://%s:%s@%s:%s", srcScheme, *srcUser, *srcPass, *srcHost, *srcPort)
	destURL := fmt.Sprintf("%s://%s:%s@%s:%s", destScheme, *destUser, *destPass, *destHost, *destPort)

	sourceConfig := ElasticConfig{URL: srcURL, Index: *srcIndex, User: *srcUser, Pass: *srcPass}
	destinationConfig := ElasticConfig{URL: destURL, Index: *destIndex, User: *destUser, Pass: *destPass}

	sourceClient, err := createClient(sourceConfig, *srcInsecure, true)
	if err != nil {
		log.Fatalf("Error creating source client: %v", err)
	}
	destClient, err := createClient(destinationConfig, false, true)
	if err != nil {
		log.Fatalf("Error creating destination client: %v", err)
	}

	ensureIndex(context.Background(), destClient, *destIndex)

	actualTotalDocs, err := getTotalDocumentCount(sourceClient, *srcIndex)
	if err != nil {
		log.Fatalf("Error getting total document count: %v", err)
	}

	if *toDoc == 0 || *toDoc > actualTotalDocs {
		*toDoc = actualTotalDocs
	}

	if err := copyData(context.Background(), sourceClient, destClient, sourceConfig, *destIndex, *fromDoc, *toDoc); err != nil {
		log.Fatalf("Error copying data: %v", err)
	}
	fmt.Println("\nData migration completed successfully.")
}

func createClient(cfg ElasticConfig, insecure bool, forceHttp1 bool) (*elastic.Client, error) {
	options := []elastic.ClientOptionFunc{
		elastic.SetURL(cfg.URL),
		elastic.SetBasicAuth(cfg.User, cfg.Pass),
		elastic.SetSniff(false),
	}

	if insecure || forceHttp1 {
		httpClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure},
				TLSNextProto:    map[string]func(string, *tls.Conn) http.RoundTripper{},
			},
		}
		options = append(options, elastic.SetHttpClient(httpClient))
	}

	return elastic.NewClient(options...)
}

func ensureIndex(ctx context.Context, client *elastic.Client, index string) {
	exists, err := client.IndexExists(index).Do(ctx)
	if err != nil {
		log.Fatalf("Error checking if index exists: %v", err)
	}
	if !exists {
		_, err = client.CreateIndex(index).Do(ctx)
		if err != nil {
			log.Fatalf("Error creating index: %v", err)
		}
	}
}

func getTotalDocumentCount(client *elastic.Client, index string) (int, error) {
	countService := client.Count(index)
	count, err := countService.Do(context.Background())
	if err != nil {
		return 0, err
	}
	return int(count), nil
}

func copyData(ctx context.Context, sourceClient, destClient *elastic.Client, srcConfig ElasticConfig, destIndex string, fromDoc int, toDoc int) error {
	totalDocs := toDoc - fromDoc + 1

	query := elastic.NewMatchAllQuery()
	scroll := sourceClient.Scroll(srcConfig.Index).Query(query).Size(batchSize)

	copiedDocs := 0

	for {
		results, err := scroll.Do(ctx)
		if err != nil {
			return fmt.Errorf("error retrieving results: %v", err)
		}
		if len(results.Hits.Hits) == 0 {
			break
		}

		bulkRequest := destClient.Bulk()
		for _, hit := range results.Hits.Hits {
			copiedDocs++
			if copiedDocs < fromDoc {
				continue
			}
			if toDoc != 0 && copiedDocs > toDoc {
				break
			}
			req := elastic.NewBulkIndexRequest().Index(destIndex).Id(hit.Id).Doc(hit.Source)
			bulkRequest = bulkRequest.Add(req)
		}

		if bulkRequest.NumberOfActions() > 0 {
			if _, err := bulkRequest.Do(ctx); err != nil {
				return fmt.Errorf("error bulk indexing: %v", err)
			}
		}

		if toDoc != 0 && copiedDocs >= toDoc {
			break
		}
		fmt.Fprintf(os.Stdout, "\rProgress: Copied %d/%d documents (%.2f%%)", copiedDocs-fromDoc+1, totalDocs, float64(copiedDocs-fromDoc+1)*100/float64(totalDocs))
	}
	return nil
}
