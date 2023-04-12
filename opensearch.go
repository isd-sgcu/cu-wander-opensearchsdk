package opensearchsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/opensearch-project/opensearch-go/v2"
	"github.com/opensearch-project/opensearch-go/v2/opensearchapi"
	"github.com/opensearch-project/opensearch-go/v2/opensearchutil"
	"github.com/pkg/errors"
	gosdk "github.com/thinc-org/newbie-gosdk"
	"github.com/thinc-org/newbie-repository"
	"go.uber.org/zap"
	"log"
	"net/http"
	"time"
)

type Suggestion struct {
	Input  []string `json:"input" mapstructure:"input"`
	Weight int      `json:"weight" mapstructure:"weight"`
}

type OpenSearchDocumentAble interface {
	ToDoc() any
	GetID() string
}

type OpenSearchRepository[T OpenSearchDocumentAble] interface {
	CreateIndex(indexName string, indexBody []byte) error
	Insert(indexName string, docID string, doc T) error
	InsertBulk(indexName string, contentList []T) error
	Update(indexName string, docID string, doc map[string]interface{}) error
	Delete(indexName string, docID string) error
	Search(indexName string, req *map[string]interface{}, result *map[string]interface{}, meta *repositorysdk.PaginationMetadata) error
	Suggest(indexName string, req *map[string]interface{}, result *map[string]interface{}) error
}

type openSearchRepository[T OpenSearchDocumentAble] struct {
	opensearchClient *opensearch.Client
	logger           *zap.Logger
}

func NewOpenSearchRepository[T OpenSearchDocumentAble](client *opensearch.Client) OpenSearchRepository[T] {
	logger, _ := gosdk.NewLogger()

	return &openSearchRepository[T]{
		opensearchClient: client,
		logger:           logger,
	}
}

func (r *openSearchRepository[T]) CreateIndex(indexName string, indexBody []byte) error {
	res, err := r.opensearchClient.Indices.Create(
		indexName,
		r.opensearchClient.Indices.Create.WithBody(bytes.NewReader(indexBody)),
	)
	if err != nil {
		return err
	}

	if res.IsError() {
		return errors.New(res.String())
	}

	return nil
}

func (r *openSearchRepository[T]) Search(indexName string, req *map[string]interface{}, result *map[string]interface{}, meta *repositorysdk.PaginationMetadata) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	(*req)["from"] = meta.GetOffset()
	(*req)["size"] = meta.GetItemPerPage()

	reqJSON, err := json.Marshal(req)
	if err != nil {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
		)

		return err
	}

	search := opensearchapi.SearchRequest{
		Index: []string{indexName},
		Body:  bytes.NewReader(reqJSON),
	}

	res, err := search.Do(ctx, r.opensearchClient)

	if err != nil {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
		)

		return err
	}

	if res.StatusCode > 200 {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
		)

		return errors.New("Invalid query")
	}
	defer res.Body.Close()

	if err := json.NewDecoder(res.Body).Decode(result); err != nil {
		return err
	}

	calMetadata(meta, result)

	return nil
}

func (r *openSearchRepository[T]) Suggest(indexName string, req *map[string]interface{}, result *map[string]interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// set maximum suggestion = 10
	(*req)["size"] = 10

	reqJSON, err := json.Marshal(req)
	if err != nil {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
		)

		return err
	}

	search := opensearchapi.SearchRequest{
		Index: []string{indexName},
		Body:  bytes.NewReader(reqJSON),
	}

	res, err := search.Do(ctx, r.opensearchClient)
	if err != nil {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
		)

		return err
	}

	if res.StatusCode > 200 {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
		)

		return errors.New("Invalid query")
	}
	defer res.Body.Close()

	if err := json.NewDecoder(res.Body).Decode(result); err != nil {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
		)
		return err
	}

	return nil
}

func (r *openSearchRepository[T]) Insert(indexName string, docID string, doc T) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := opensearchapi.IndexRequest{
		Index:      indexName,
		DocumentID: docID,
		Body:       opensearchutil.NewJSONReader(doc.ToDoc()),
	}

	res, err := req.Do(ctx, r.opensearchClient)
	if err != nil {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
			zap.String("doc_id", docID),
		)
		return err
	}

	if res.StatusCode >= http.StatusBadRequest {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
			zap.String("doc_id", docID),
			zap.Int("status_code", res.StatusCode),
		)
		return errors.New("insert failed")
	}

	r.logger.Info(
		"successfully insert document",
		zap.String("index_name", indexName),
		zap.String("doc_id", docID),
	)

	return nil
}

func (r *openSearchRepository[T]) InsertBulk(indexName string, contentList []T) error {
	// Initialize indexer
	indexer, err := opensearchutil.NewBulkIndexer(opensearchutil.BulkIndexerConfig{
		Client: r.opensearchClient,
		Index:  indexName,
	})
	if err != nil {
		log.Fatalf("Error creating the indexer: %s", err)
	}

	for _, content := range contentList {
		insertBulk(indexer, r.logger, indexName, content)
	}

	// Close the indexer channel and flush remaining items
	if err := indexer.Close(context.Background()); err != nil {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
		)
	}

	// Report the indexer statistics
	stats := indexer.Stats()
	if stats.NumFailed > 0 {
		r.logger.Error(
			"inserting some document failed",
			zap.Error(errors.New("inserting some document failed")),
			zap.String("index_name", indexName),
			zap.Uint64("num_flush", stats.NumFlushed),
			zap.Uint64("num_failed", stats.NumFailed),
		)
	} else {
		r.logger.Info(
			"successfully insert bulk document",
			zap.String("index_name", indexName),
		)
	}

	return nil
}

func (r *openSearchRepository[T]) Update(indexName string, docID string, doc map[string]interface{}) error {
	res, err := r.opensearchClient.Update(indexName, docID, opensearchutil.NewJSONReader(map[string]interface{}{"doc": doc}), r.opensearchClient.Update.WithTimeout(5*time.Second))
	if err != nil {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
			zap.String("doc_id", docID),
		)
		return err
	}

	if res.StatusCode >= http.StatusBadRequest {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
			zap.String("doc_id", docID),
			zap.Int("status_code", res.StatusCode),
		)

		return errors.New("update failed")
	}

	r.logger.Info(
		"successfully update document",
		zap.String("index_name", indexName),
		zap.String("doc_id", docID),
	)

	return nil
}

func (r *openSearchRepository[T]) Delete(indexName string, docID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := opensearchapi.DeleteRequest{
		Index:      indexName,
		DocumentID: docID,
	}

	res, err := req.Do(ctx, r.opensearchClient)
	if err != nil {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
			zap.String("doc_id", docID),
		)
		return err
	}

	if res.StatusCode >= http.StatusBadRequest {
		r.logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
			zap.String("doc_id", docID),
			zap.Int("status_code", res.StatusCode),
		)
		return errors.New("delete failed")
	}

	r.logger.Info(
		"successfully delete document",
		zap.Error(err),
		zap.String("index_name", indexName),
		zap.String("doc_id", docID),
	)
	return nil
}

func insertBulk[T OpenSearchDocumentAble](indexer opensearchutil.BulkIndexer, logger *zap.Logger, indexName string, content T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	docContent, err := json.Marshal(content.ToDoc())
	if err != nil {
		logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
			zap.String("doc_id", content.GetID()),
		)
	}

	if err := indexer.Add(
		ctx,
		opensearchutil.BulkIndexerItem{
			Index:      indexName,
			Action:     "index",
			DocumentID: content.GetID(),
			Body:       bytes.NewReader(docContent),
			OnSuccess: func(
				ctx context.Context,
				item opensearchutil.BulkIndexerItem,
				res opensearchutil.BulkIndexerResponseItem) {
				logger.Info(
					"successfully insert doc content",
					zap.Error(err),
					zap.String("index_name", indexName),
					zap.String("doc_id", content.GetID()),
				)
			},
			OnFailure: func(
				ctx context.Context,
				item opensearchutil.BulkIndexerItem,
				res opensearchutil.BulkIndexerResponseItem,
				err error) {
				if err != nil {
					logger.Error(
						err.Error(),
						zap.Error(err),
						zap.String("index_name", indexName),
						zap.String("doc_id", content.GetID()),
					)
				} else {
					logger.Error(
						err.Error(),
						zap.Error(err),
						zap.String("index_name", indexName),
						zap.String("doc_id", content.GetID()),
						zap.String("error_type", res.Error.Type),
						zap.String("error_reason", res.Error.Reason),
					)
				}
			},
		},
	); err != nil {
		logger.Error(
			err.Error(),
			zap.Error(err),
			zap.String("index_name", indexName),
			zap.String("doc_id", content.GetID()),
		)
	}
}

func calMetadata(meta *repositorysdk.PaginationMetadata, result *map[string]interface{}) {
	hits := (*result)["hits"].(map[string]interface{})
	totalItemValue := int(hits["total"].(map[string]interface{})["value"].(float64))

	meta.TotalItem = totalItemValue
	meta.TotalPage = totalItemValue / meta.ItemsPerPage
	meta.ItemCount = len(hits["hits"].([]interface{}))

	// Add total item by 1 if cannot divisible by item per page
	if totalItemValue%meta.ItemsPerPage != 0 {
		meta.TotalPage++
	}
}
