package cluster

import "github.com/humpback/discovery"
import "github.com/humpback/discovery/backends"
import "github.com/humpback/gounits/json"
import "github.com/humpback/gounits/logger"
import "github.com/humpback/humpback-center/cluster/types"

import (
	"errors"
	"sync"
)

// Cluster errors define
var (
	//ErrClusterDiscoveryInvalid, discovery is nil.
	ErrClusterDiscoveryInvalid = errors.New("cluster discovery invalid.")
)

// Group is exported
// Servers: cluster server ips, correspond engines's key.
type Group struct {
	ID      string
	Servers []string
}

// Cluster is exported
// engines: map[ip]*Engine
// groups:  map[groupid]*Group
type Cluster struct {
	sync.RWMutex
	Discovery *discovery.Discovery
	engines   map[string]*Engine
	groups    map[string]*Group
	stopCh    chan struct{}
}

// NewCluster is exported
func NewCluster(discovery *discovery.Discovery) (*Cluster, error) {

	if discovery == nil {
		return nil, ErrClusterDiscoveryInvalid
	}

	return &Cluster{
		Discovery: discovery,
		engines:   make(map[string]*Engine),
		groups:    make(map[string]*Group),
		stopCh:    make(chan struct{}),
	}, nil
}

func (cluster *Cluster) Start() error {

	logger.INFO("[#cluster#] cluster discovery watching...")
	if cluster.Discovery != nil {
		cluster.Discovery.Watch(cluster.stopCh, cluster.watchHandleFunc)
		return nil
	}
	return ErrClusterDiscoveryInvalid
}

func (cluster *Cluster) Stop() {

	close(cluster.stopCh)
	logger.INFO("[#cluster#] cluster discovery closed.")
}

func (cluster *Cluster) GetEngine(ip string) *Engine {

	cluster.RLock()
	defer cluster.RUnlock()
	if engine, ret := cluster.engines[ip]; ret {
		return engine
	}
	return nil
}

func (cluster *Cluster) GetGroups() []*Group {

	groups := []*Group{}
	cluster.RLock()
	for _, group := range cluster.groups {
		groups = append(groups, group)
	}
	cluster.RUnlock()
	return groups
}

func (cluster *Cluster) GetGroup(groupid string) *Group {

	cluster.RLock()
	defer cluster.RUnlock()
	if group, ret := cluster.groups[groupid]; ret {
		return group
	}
	return nil
}

func (cluster *Cluster) SetGroup(groupid string, servers []string) {

	cluster.Lock()
	removes := []string{}
	group, ret := cluster.groups[groupid]
	if !ret {
		group = &Group{ID: groupid, Servers: servers}
		cluster.groups[groupid] = group
		logger.INFO("[#cluster#] cluster create group %s(%d)", groupid, len(servers))
	} else {
		origin := group.Servers
		group.Servers = servers
		for _, oldip := range origin {
			found := false
			for _, newip := range group.Servers {
				if oldip == newip {
					found = true
					break
				}
			}
			if !found {
				removes = append(removes, oldip)
			}
		}
		logger.INFO("[#cluster#] cluster set group %s(%d)", groupid, len(servers))
	}
	cluster.Unlock()

	for _, ip := range servers {
		cluster.addEngine(ip)
	}

	for _, ip := range removes {
		cluster.removeEngine(ip)
	}
}

func (cluster *Cluster) RemoveGroup(groupid string) bool {

	cluster.Lock()
	defer cluster.Unlock()
	group, ret := cluster.groups[groupid]
	if !ret {
		logger.WARN("[#cluster#] cluster remove group %s not found.", groupid)
		return false
	}
	logger.INFO("[#cluster#] cluster remove group %s(%d)", groupid, len(group.Servers))
	delete(cluster.groups, groupid)
	return true
}

func (cluster *Cluster) watchHandleFunc(added backends.Entries, removed backends.Entries, err error) {

	if err != nil {
		logger.ERROR("[#cluster#] cluster discovery watch error:%s", err.Error())
		return
	}

	logger.INFO("[#cluster#] cluster discovery watch handler removed:%d added:%d.", len(removed), len(added))
	opts := &types.ClusterRegistOptions{}
	for _, entry := range removed {
		if err := json.DeCodeBufferToObject(entry.Data, opts); err != nil {
			logger.ERROR("[#cluster#] cluster discovery handlefunc error: removed, %s", err.Error())
			continue
		}
		logger.INFO("[#cluster#] cluster discovery removed:%s.", entry.Key)
		if engine := cluster.removeEngine(opts.IP); engine != nil {
			engine.SetRegistOptions(entry.Key, nil)
			engine.SetState(stateUnhealthy)
			logger.INFO("[#cluster#] cluster set engine %p:%s %s", engine, engine.IP, engine.State())
		}
	}

	for _, entry := range added {
		if err := json.DeCodeBufferToObject(entry.Data, opts); err != nil {
			logger.ERROR("[#cluster#] cluster discovery handlefunc error: added, %s", err.Error())
			continue
		}
		logger.INFO("[#cluster#] cluster discovery added:%s.", entry.Key)
		if engine := cluster.addEngine(opts.IP); engine != nil {
			engine.SetRegistOptions(entry.Key, opts)
			engine.SetState(stateHealthy)
			logger.INFO("[#cluster#] cluster set engine %p:%s %s", engine, engine.IP, engine.State())
		}
	}
}

func (cluster *Cluster) addEngine(ip string) *Engine {

	engine := cluster.GetEngine(ip)
	if engine == nil {
		var err error
		if engine, err = NewEngine(ip); err != nil {
			logger.ERROR("[#cluster#] cluster add engine %s error:%s", ip, err.Error())
			return nil
		}
		cluster.Lock()
		cluster.engines[ip] = engine
		cluster.Unlock()
		logger.INFO("[#cluster#] cluster add engine %p:%s", engine, ip)
	}
	return engine
}

func (cluster *Cluster) removeEngine(ip string) *Engine {

	engine := cluster.GetEngine(ip)
	if engine == nil {
		logger.WARN("[#cluster#] cluster remove engine, not found:%s", ip)
		return nil
	}

	found := false
	groups := cluster.GetGroups()
	for _, group := range groups {
		if !found {
			for _, ip := range group.Servers {
				if ip == engine.IP {
					found = true
					break
				}
			}
		}
	}

	if !found {
		engine.Close() //close engine
		cluster.Lock()
		delete(cluster.engines, engine.IP)
		cluster.Unlock()
		logger.INFO("[#cluster#] cluster remove engine %p:%s", engine, engine.IP)
	}
	return engine
}
