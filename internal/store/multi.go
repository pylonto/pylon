package store

import (
	"encoding/json"
	"sync"
)

// MultiStore routes job operations to per-pylon Store instances.
// It satisfies the same method set as Store, using PylonName to route writes
// and searching across all stores for reads.
type MultiStore struct {
	mu     sync.RWMutex
	stores map[string]*Store // pylon name -> store
	index  map[string]string // job ID -> pylon name (for fast lookup)
}

// NewMulti creates a MultiStore from a map of pylon name -> Store.
func NewMulti(stores map[string]*Store) *MultiStore {
	return &MultiStore{
		stores: stores,
		index:  make(map[string]string),
	}
}

func (m *MultiStore) storeFor(pylonName string) *Store {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stores[pylonName]
}

func (m *MultiStore) storeForJob(jobID string) *Store {
	m.mu.RLock()
	pylonName, ok := m.index[jobID]
	m.mu.RUnlock()
	if ok {
		return m.storeFor(pylonName)
	}
	return nil
}

func (m *MultiStore) Put(j *Job) {
	m.mu.Lock()
	m.index[j.ID] = j.PylonName
	m.mu.Unlock()
	if s := m.storeFor(j.PylonName); s != nil {
		s.Put(j)
	}
}

func (m *MultiStore) Get(id string) (*Job, bool) {
	if s := m.storeForJob(id); s != nil {
		return s.Get(id)
	}
	// Fallback: search all stores
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.stores {
		if j, ok := s.Get(id); ok {
			return j, true
		}
	}
	return nil, false
}

func (m *MultiStore) GetByTopic(topicID string) (*Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.stores {
		if j, ok := s.GetByTopic(topicID); ok {
			return j, true
		}
	}
	return nil, false
}

func (m *MultiStore) List() []*Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []*Job
	for _, s := range m.stores {
		all = append(all, s.List()...)
	}
	return all
}

func (m *MultiStore) UpdateStatus(id, status string) {
	if s := m.storeForJob(id); s != nil {
		s.UpdateStatus(id, status)
	}
}

func (m *MultiStore) UpdateSessionID(id, sessionID string) {
	if s := m.storeForJob(id); s != nil {
		s.UpdateSessionID(id, sessionID)
	}
}

func (m *MultiStore) SetCompleted(id string, output json.RawMessage) {
	if s := m.storeForJob(id); s != nil {
		s.SetCompleted(id, output)
	}
}

func (m *MultiStore) SetFailed(id, errMsg string) {
	if s := m.storeForJob(id); s != nil {
		s.SetFailed(id, errMsg)
	}
}

func (m *MultiStore) Delete(id string) {
	if s := m.storeForJob(id); s != nil {
		s.Delete(id)
	}
	m.mu.Lock()
	delete(m.index, id)
	m.mu.Unlock()
}

func (m *MultiStore) SavePayloadSample(pylonName string, body map[string]interface{}) {
	if s := m.storeFor(pylonName); s != nil {
		s.SavePayloadSample(pylonName, body)
	}
}

func (m *MultiStore) RecoverFromDB() int {
	m.mu.RLock()
	storesCopy := make(map[string]*Store, len(m.stores))
	for k, v := range m.stores {
		storesCopy[k] = v
	}
	m.mu.RUnlock()

	total := 0
	for _, s := range storesCopy {
		n := s.RecoverFromDB()
		total += n
		for _, j := range s.List() {
			m.mu.Lock()
			m.index[j.ID] = j.PylonName
			m.mu.Unlock()
		}
	}
	return total
}
