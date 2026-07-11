package fabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

var ErrTransferNotFound = errors.New("transfer_not_found")
var ErrTransferInvalid = errors.New("transfer_invalid")
var ErrTransferChunkConflict = errors.New("transfer_chunk_conflict")
var ErrTransferIncomplete = errors.New("transfer_incomplete")
var ErrTransferDigestMismatch = errors.New("transfer_digest_mismatch")
var ErrContentNotFound = errors.New("content_not_found")

type TransferInput struct {
	OrganizationID string `json:"organizationId"`
	WorkspaceID    string `json:"workspaceId"`
	ProjectID      string `json:"projectId"`
	Path           string `json:"path"`
	Digest         string `json:"digest"`
	Size           int64  `json:"size"`
	ChunkSize      int    `json:"chunkSize,omitempty"`
	IdempotencyKey string `json:"-"`
}

type Transfer struct {
	TransferID     string     `json:"transferId"`
	OrganizationID string     `json:"organizationId"`
	WorkspaceID    string     `json:"workspaceId"`
	ProjectID      string     `json:"projectId"`
	Path           string     `json:"path"`
	Digest         string     `json:"digest"`
	Size           int64      `json:"size"`
	ChunkSize      int        `json:"chunkSize"`
	ChunkCount     int        `json:"chunkCount"`
	ReceivedChunks []int      `json:"receivedChunks"`
	Status         string     `json:"status"`
	IdempotencyKey string     `json:"-"`
	RequestHash    string     `json:"-"`
	CreatedAt      time.Time  `json:"createdAt"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
}

type TransferChunk struct {
	Index  int
	Digest string
	Body   []byte
}

type Content struct {
	Digest      string
	WorkspaceID string
	Path        string
	Body        []byte
}

type TransferStore interface {
	CreateTransfer(context.Context, Transfer) (Transfer, error)
	Transfer(context.Context, string) (Transfer, error)
	SaveTransfer(context.Context, Transfer) error
	SaveTransferChunk(context.Context, string, TransferChunk) error
	TransferChunks(context.Context, string) ([]TransferChunk, error)
	Content(context.Context, string, string) (Content, error)
}

type memoryTransferStore struct{ *MemoryOperationStore }

func newMemoryTransferStore() TransferStore { return memoryTransferStore{NewMemoryOperationStore()} }

func (s *MemoryOperationStore) CreateTransfer(_ context.Context, transfer Transfer) (Transfer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id := s.transferKeys[transfer.IdempotencyKey]; id != "" {
		existing := s.transferSessions[id]
		if existing.RequestHash != transfer.RequestHash {
			return Transfer{}, ErrTransferChunkConflict
		}
		return existing, nil
	}
	s.transferSessions[transfer.TransferID] = transfer
	s.transferKeys[transfer.IdempotencyKey] = transfer.TransferID
	return transfer, nil
}

func (s *MemoryOperationStore) Transfer(_ context.Context, id string) (Transfer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	transfer, ok := s.transferSessions[id]
	if !ok {
		return Transfer{}, ErrTransferNotFound
	}
	transfer.ReceivedChunks = receivedIndexes(s.transferChunks[id])
	return transfer, nil
}

func (s *MemoryOperationStore) SaveTransfer(_ context.Context, transfer Transfer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.transferSessions[transfer.TransferID]; !ok {
		return ErrTransferNotFound
	}
	s.transferSessions[transfer.TransferID] = transfer
	return nil
}

func (s *MemoryOperationStore) SaveTransferChunk(_ context.Context, id string, chunk TransferChunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.transferSessions[id]; !ok {
		return ErrTransferNotFound
	}
	if s.transferChunks[id] == nil {
		s.transferChunks[id] = map[int]TransferChunk{}
	}
	if existing, ok := s.transferChunks[id][chunk.Index]; ok {
		if existing.Digest != chunk.Digest {
			return ErrTransferChunkConflict
		}
		return nil
	}
	chunk.Body = append([]byte(nil), chunk.Body...)
	s.transferChunks[id][chunk.Index] = chunk
	return nil
}

func (s *MemoryOperationStore) TransferChunks(_ context.Context, id string) ([]TransferChunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.transferSessions[id]; !ok {
		return nil, ErrTransferNotFound
	}
	indexes := receivedIndexes(s.transferChunks[id])
	chunks := make([]TransferChunk, 0, len(indexes))
	for _, index := range indexes {
		chunks = append(chunks, s.transferChunks[id][index])
	}
	return chunks, nil
}

func (s *MemoryOperationStore) Content(_ context.Context, workspaceID, digest string) (Content, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, transfer := range s.transferSessions {
		if transfer.Status != "completed" || transfer.WorkspaceID != workspaceID || transfer.Digest != digest {
			continue
		}
		var body []byte
		for _, index := range receivedIndexes(s.transferChunks[id]) {
			body = append(body, s.transferChunks[id][index].Body...)
		}
		return Content{Digest: digest, WorkspaceID: transfer.WorkspaceID, Path: transfer.Path, Body: body}, nil
	}
	return Content{}, ErrContentNotFound
}

type workspaceContentPublisher interface {
	PublishWorkspaceContent(context.Context, string, string, []byte) error
}

func (s *Service) CreateTransfer(ctx context.Context, input TransferInput) (Transfer, error) {
	input.Path = path.Clean(strings.TrimSpace(input.Path))
	if input.OrganizationID == "" || input.WorkspaceID == "" || input.ProjectID == "" || input.IdempotencyKey == "" || input.Size < 0 || !validDigest(input.Digest) || input.Path == "." || strings.HasPrefix(input.Path, "../") || path.IsAbs(input.Path) {
		return Transfer{}, ErrTransferInvalid
	}
	input.ChunkSize = 4 << 20
	chunkCount := int((input.Size + int64(input.ChunkSize) - 1) / int64(input.ChunkSize))
	now := s.now()
	requestHash := hashInput(input)
	transfer := Transfer{TransferID: fabricID("transfer", input.WorkspaceID+input.Digest, now), OrganizationID: input.OrganizationID, WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID, Path: input.Path, Digest: strings.ToLower(input.Digest), Size: input.Size, ChunkSize: input.ChunkSize, ChunkCount: chunkCount, Status: "uploading", IdempotencyKey: input.IdempotencyKey, RequestHash: requestHash, CreatedAt: now}
	return s.transfers.CreateTransfer(ctx, transfer)
}

func (s *Service) Transfer(ctx context.Context, id string) (Transfer, error) {
	return s.transfers.Transfer(ctx, id)
}

func (s *Service) PutTransferChunk(ctx context.Context, id string, index int, body []byte, digest string) (Transfer, error) {
	transfer, err := s.transfers.Transfer(ctx, id)
	if err != nil {
		return Transfer{}, err
	}
	if transfer.Status == "completed" || index < 0 || index >= transfer.ChunkCount || !validDigest(digest) || fmt.Sprintf("%x", sha256.Sum256(body)) != strings.ToLower(digest) || len(body) > transfer.ChunkSize || (index < transfer.ChunkCount-1 && len(body) != transfer.ChunkSize) {
		return Transfer{}, ErrTransferInvalid
	}
	if err := s.transfers.SaveTransferChunk(ctx, id, TransferChunk{Index: index, Digest: strings.ToLower(digest), Body: body}); err != nil {
		return Transfer{}, err
	}
	return s.transfers.Transfer(ctx, id)
}

func (s *Service) CompleteTransfer(ctx context.Context, id string) (Transfer, error) {
	transfer, err := s.transfers.Transfer(ctx, id)
	if err != nil {
		return Transfer{}, err
	}
	if transfer.Status == "completed" {
		return transfer, nil
	}
	chunks, err := s.transfers.TransferChunks(ctx, id)
	if err != nil {
		return Transfer{}, err
	}
	if len(chunks) != transfer.ChunkCount {
		return Transfer{}, ErrTransferIncomplete
	}
	var body []byte
	// ponytail: assemble DB-backed chunks in one process until measured file sizes justify object storage.
	for i, chunk := range chunks {
		if chunk.Index != i {
			return Transfer{}, ErrTransferIncomplete
		}
		body = append(body, chunk.Body...)
	}
	if int64(len(body)) != transfer.Size || fmt.Sprintf("%x", sha256.Sum256(body)) != transfer.Digest {
		return Transfer{}, ErrTransferDigestMismatch
	}
	publisher, ok := s.provider.(workspaceContentPublisher)
	if !ok {
		return Transfer{}, errors.New("content_publish_unavailable")
	}
	if err := publisher.PublishWorkspaceContent(ctx, transfer.WorkspaceID, transfer.Path, body); err != nil {
		return Transfer{}, err
	}
	now := s.now()
	transfer.Status = "completed"
	transfer.CompletedAt = &now
	transfer.ReceivedChunks = receivedIndexesFromChunks(chunks)
	if err := s.transfers.SaveTransfer(ctx, transfer); err != nil {
		return Transfer{}, err
	}
	return transfer, nil
}

func (s *Service) Content(ctx context.Context, workspaceID, digest string) (Content, error) {
	if workspaceID == "" || !validDigest(digest) {
		return Content{}, ErrTransferInvalid
	}
	return s.transfers.Content(ctx, workspaceID, strings.ToLower(digest))
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
func receivedIndexes(chunks map[int]TransferChunk) []int {
	indexes := make([]int, 0, len(chunks))
	for index := range chunks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes
}
func receivedIndexesFromChunks(chunks []TransferChunk) []int {
	indexes := make([]int, len(chunks))
	for i := range chunks {
		indexes[i] = chunks[i].Index
	}
	return indexes
}
