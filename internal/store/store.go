package store

import (
	"fmt"
	"sync"
	"time"

	"github.com/yuno/ai-challenge/internal/model"
)

// Store is a thread-safe in-memory transaction store.
type Store struct {
	mu           sync.RWMutex
	transactions map[string]*model.Transaction
}

// New creates a new Store.
func New() *Store {
	return &Store{
		transactions: make(map[string]*model.Transaction),
	}
}

// Create stores a new transaction. Returns error if ID already exists.
func (s *Store) Create(txn *model.Transaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.transactions[txn.ID]; exists {
		return fmt.Errorf("transaction %s already exists", txn.ID)
	}
	s.transactions[txn.ID] = txn
	return nil
}

// Get retrieves a transaction by ID.
func (s *Store) Get(id string) (*model.Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	txn, ok := s.transactions[id]
	if !ok {
		return nil, fmt.Errorf("transaction %s not found", id)
	}
	// Return a copy to prevent race conditions on reads.
	cp := *txn
	cp.Attempts = make([]model.Attempt, len(txn.Attempts))
	copy(cp.Attempts, txn.Attempts)
	cp.ProcessorOrder = make([]string, len(txn.ProcessorOrder))
	copy(cp.ProcessorOrder, txn.ProcessorOrder)
	return &cp, nil
}

// AddAttempt appends an attempt to a transaction.
func (s *Store) AddAttempt(id string, attempt model.Attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	txn, ok := s.transactions[id]
	if !ok {
		return fmt.Errorf("transaction %s not found", id)
	}
	txn.Attempts = append(txn.Attempts, attempt)
	return nil
}

// UpdateStatus sets the final status, processor used, error, and completion timestamp.
func (s *Store) UpdateStatus(id string, status model.TransactionStatus, processorUsed, finalError string, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	txn, ok := s.transactions[id]
	if !ok {
		return fmt.Errorf("transaction %s not found", id)
	}
	txn.Status = status
	txn.ProcessorUsed = processorUsed
	txn.FinalError = finalError
	txn.CompletedAt = completedAt
	txn.TotalProcessingTimeMs = float64(completedAt.Sub(txn.CreatedAt).Nanoseconds()) / 1e6
	return nil
}

// List returns all transactions (copies).
func (s *Store) List() []*model.Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*model.Transaction, 0, len(s.transactions))
	for _, txn := range s.transactions {
		cp := *txn
		cp.Attempts = make([]model.Attempt, len(txn.Attempts))
		copy(cp.Attempts, txn.Attempts)
		result = append(result, &cp)
	}
	return result
}
