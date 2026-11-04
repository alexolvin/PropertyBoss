package scan

import (
	"fmt"
	"sync"
)

// Реестр коннекторов по id источника. Конкретные коннекторы регистрируют
// свои пакеты в init() (blank-import из cmd/pb).
var (
	regMu      sync.RWMutex
	connectors = map[string]Connector{}
)

// Register — регистрация коннектора. Дубликат для того же source — паника
// (ошибка сборки логики, а не рантайма).
func Register(c Connector) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, ok := connectors[c.SourceID()]; ok {
		panic(fmt.Sprintf("scan: коннектор для источника %s уже зарегистрирован", c.SourceID()))
	}
	connectors[c.SourceID()] = c
}

// Get — коннектор для источника; false, если для него коннектор не написан.
func Get(sourceID string) (Connector, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	c, ok := connectors[sourceID]
	return c, ok
}

// SourceIDs — зарегистрированные источники (для диагностики, напр. `pb scan --list`).
func SourceIDs() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	ids := make([]string, 0, len(connectors))
	for id := range connectors {
		ids = append(ids, id)
	}
	return ids
}
