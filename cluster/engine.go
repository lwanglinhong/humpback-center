package cluster

import "common/models"
import "github.com/humpback/gounits/convert"
import "github.com/humpback/gounits/logger"
import "github.com/humpback/gounits/rand"
import "github.com/humpback/gounits/utils"

import (
	"context"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// remove container fail max value.
	maxRemoveFailThreshold = 2
	// delay remove containers loop interval
	delayRemoveInterval = 15 * time.Second
	// engine refresh loop interval
	refreshInterval = 45 * time.Second
)

// Engine state define
type engineState int

// State enum value
const (
	//pending: engine added to cluster engines pool, but not been validated.
	StatePending engineState = iota
	//unhealthy: engine is unreachable.
	StateUnhealthy
	//healthy: engine is ready reachable.
	StateHealthy
	//disconnected: engine is removed from discovery
	StateDisconnected
)

// Engine state content mapping
var stateText = map[engineState]string{
	StatePending:      "Pending",
	StateUnhealthy:    "Unhealthy",
	StateHealthy:      "Healthy",
	StateDisconnected: "Disconnected",
}

// GetStateText is exported return a state typed text.
func GetStateText(state engineState) string {
	return stateText[state]
}

// RemoveContainer is exported
// remove pool container info.
type RemoveContainer struct {
	metaID      string
	containerID string
	timeStamp   int64
	failCount   int
}

// RemovePool is exported
type RemovePool struct {
	sync.Mutex
	removeDelay time.Duration
	containers  map[string]*RemoveContainer
}

// Engine is exported
type Engine struct {
	sync.RWMutex
	ID        string            `json:"ID"`
	Name      string            `json:"Name"`
	IP        string            `json:"IP"`
	APIAddr   string            `json:"APIAddr"`
	Cpus      int64             `json:"Cpus"`
	Memory    int64             `json:"Memory"`
	Labels    map[string]string `json:"Labels"`
	StateText string            `json:"StateText"`

	overcommitRatio int64
	client          *Client
	removePool      *RemovePool
	configCache     *ContainersConfigCache
	containers      map[string]*Container
	stopCh          chan struct{}
	state           engineState
}

// NewEngine is exported
func NewEngine(nodeData *NodeData, overcommitRatio float64, removeDelay time.Duration, configCache *ContainersConfigCache) (*Engine, error) {

	ipAddr, err := net.ResolveIPAddr("ip4", nodeData.IP)
	if err != nil {
		return nil, err
	}

	removePool := &RemovePool{
		removeDelay: removeDelay,
		containers:  make(map[string]*RemoveContainer),
	}

	return &Engine{
		ID:              nodeData.ID,
		Name:            nodeData.Name,
		IP:              ipAddr.IP.String(),
		APIAddr:         nodeData.APIAddr,
		Cpus:            nodeData.Cpus,
		Memory:          int64(math.Ceil(float64(nodeData.Memory) / 1024.0 / 1024.0)),
		Labels:          nodeData.MapLabels(),
		StateText:       stateText[StatePending],
		overcommitRatio: int64(overcommitRatio * 100),
		client:          NewClient(nodeData.APIAddr),
		removePool:      removePool,
		configCache:     configCache,
		containers:      make(map[string]*Container),
		state:           StatePending,
	}, nil
}

// Open is exported
// Engine start refresh containers loop
func (engine *Engine) Open() {

	engine.Lock()
	if engine.state != StateHealthy {
		engine.state = StateHealthy
		engine.StateText = stateText[engine.state]
		engine.stopCh = make(chan struct{})
		logger.INFO("[#cluster#] engine %s open.", engine.IP)
		go func() {
			engine.ValidateContainers()
			engine.refreshContainersLoop()
		}()
		go engine.removeContainersDelayLoop()
	}
	engine.Unlock()
}

// Close is exported
// Engine stop refresh containers loop
func (engine *Engine) Close() {

	engine.Lock()
	if engine.state == StateHealthy {
		close(engine.stopCh)
		engine.state = StateDisconnected
		engine.StateText = stateText[engine.state]
		engine.client.Close()
		logger.INFO("[#cluster#] engine %s closed.", engine.IP)
	}
	engine.Unlock()
}

// Update is exported
// Engine update info
func (engine *Engine) Update(nodeData *NodeData) {

	engine.Lock()
	engine.ID = nodeData.ID
	engine.Name = nodeData.Name
	engine.APIAddr = nodeData.APIAddr
	engine.Cpus = nodeData.Cpus
	engine.Memory = int64(math.Ceil(float64(nodeData.Memory) / 1024.0 / 1024.0))
	engine.Labels = nodeData.MapLabels()
	engine.Unlock()
}

// IsHealthy is exported
// Determine if the engine is in healthy state
func (engine *Engine) IsHealthy() bool {

	engine.RLock()
	defer engine.RUnlock()
	return engine.state == StateHealthy
}

// IsPending is exported
// Determine if the engine is in pending state
func (engine *Engine) IsPending() bool {

	engine.RLock()
	defer engine.RUnlock()
	return engine.state == StatePending
}

// IsDisconnected is exported
// Determine if the engine is in disconnected state
func (engine *Engine) IsDisconnected() bool {

	engine.RLock()
	defer engine.RUnlock()
	return engine.state == StateDisconnected
}

// State is exported
// Return a engine status string result.
func (engine *Engine) State() string {

	engine.RLock()
	defer engine.RUnlock()
	return stateText[engine.state]
}

// SetState is exported
// Set engine state.
func (engine *Engine) SetState(state engineState) {

	engine.Lock()
	engine.state = state
	engine.StateText = stateText[state]
	engine.Unlock()
}

// MetaIds is exported
// Return engine containers all metaids array.
func (engine *Engine) MetaIds() []string {

	ids := []string{}
	engine.RLock()
	for _, container := range engine.containers {
		if metaid := container.MetaID(); metaid != "" {
			if ret := utils.Contains(metaid, ids); !ret {
				ids = append(ids, metaid)
			}
		}
	}
	engine.RUnlock()
	return ids
}

// HasMeta is exported
// find metaid in engine containers
func (engine *Engine) HasMeta(metaid string) bool {

	engine.RLock()
	defer engine.RUnlock()
	for _, container := range engine.containers {
		if container.MetaID() == metaid {
			return true
		}
	}
	return false
}

// HasContainer is exported
// find containerid in engine containers
func (engine *Engine) HasContainer(containerid string) bool {

	engine.RLock()
	defer engine.RUnlock()
	for _, container := range engine.containers {
		if container.Info.ID == containerid {
			return true
		}
	}
	return false
}

// Containers is exported
// Return engine containers.
// if metaid is empty string so return engine's all containers
func (engine *Engine) Containers(metaid string) Containers {

	engine.RLock()
	containers := Containers{}
	for _, container := range engine.containers {
		if metaid == "" || container.MetaID() == metaid {
			containers = append(containers, container)
		}
	}
	engine.RUnlock()
	return containers
}

// Container is exported
// Return engine container of containerid.
func (engine *Engine) Container(containerid string) *Container {

	engine.RLock()
	defer engine.RUnlock()
	for _, container := range engine.containers {
		if container.Info.ID == containerid {
			return container
		}
	}
	return nil
}

// UsedMemory is exported
// Return engine all containers used memory size.
func (engine *Engine) UsedMemory() int64 {

	var used int64
	engine.RLock()
	for _, container := range engine.containers {
		used += container.Info.HostConfig.Memory
	}
	engine.RUnlock()
	return used
}

// UsedCpus is exported
// Return engine all containers used cpus size.
func (engine *Engine) UsedCpus() int64 {

	var used int64
	engine.RLock()
	for _, container := range engine.containers {
		used += container.Info.HostConfig.CPUShares
	}
	engine.RUnlock()
	return used
}

// TotalMemory is exported
// Return engine total memory size.
func (engine *Engine) TotalMemory() int64 {

	engine.RLock()
	defer engine.RUnlock()
	return engine.Memory + (engine.Memory * engine.overcommitRatio / 100)
}

// TotalCpus is exported
// Return engine total cpus size.
func (engine *Engine) TotalCpus() int64 {

	engine.RLock()
	defer engine.RUnlock()
	return engine.Cpus + (engine.Cpus * engine.overcommitRatio / 100)
}

// CreateContainer is exported
// Engine create a container
func (engine *Engine) CreateContainer(config models.Container) (*Container, error) {

	createContainerResponse, err := engine.client.CreateContainerRequest(context.Background(), config)
	if err != nil {
		return nil, err
	}

	config.ID = createContainerResponse.ID
	configEnvMap := convert.ConvertKVStringSliceToMap(config.Env)
	containerIndex, _ := strconv.Atoi(configEnvMap["HUMPBACK_CLUSTER_CONTAINER_INDEX"])
	metaData := engine.configCache.GetMetaData(configEnvMap["HUMPBACK_CLUSTER_METAID"])
	if metaData == nil {
		return nil, ErrClusterMetaDataNotFound
	}
	baseConfig := &ContainerBaseConfig{Index: containerIndex, Container: config, MetaData: metaData}
	engine.configCache.CreateContainerBaseConfig(metaData.MetaID, baseConfig)
	logger.INFO("[#cluster#] engine %s create container %s:%s", engine.IP, ShortContainerID(createContainerResponse.ID), config.Name)
	containers, err := engine.updateContainer(createContainerResponse.ID, engine.containers)
	if err != nil {
		return nil, err
	}

	engine.Lock()
	engine.containers = containers
	container, ret := engine.containers[createContainerResponse.ID]
	if !ret {
		engine.Unlock()
		return nil, fmt.Errorf("created container, update container info failure")
	}
	engine.Unlock()
	return container, nil
}

// RemoveContainer is exported
// Engine remove a container.
func (engine *Engine) RemoveContainer(containerid string) error {

	container := engine.Container(containerid)
	if container == nil {
		return fmt.Errorf("remove container %s not found", ShortContainerID(containerid))
	}

	//rename engine container, append '-expel' suffix.
	if ret := strings.HasSuffix(container.Info.Name, "-expel"); !ret {
		expelName := strings.TrimPrefix(container.Info.Name, "/") + "-" + rand.UUID(true)[:8] + "-expel"
		operate := models.ContainerOperate{Action: "rename", Container: container.Info.ID, NewName: expelName}
		if err := engine.OperateContainer(operate); err != nil {
			logger.ERROR("[#cluster#] engine %s container %s expel, rename error:%s", engine.IP, ShortContainerID(container.Info.ID), err.Error())
		} else {
			logger.WARN("[#cluster#] engine %s container %s expel, rename to %s.", engine.IP, ShortContainerID(container.Info.ID), operate.NewName)
		}
	}

	//remove engine local metabase of container.
	engine.Lock()
	engine.configCache.RemoveContainerBaseConfig(container.MetaID(), containerid)
	delete(engine.containers, containerid)
	engine.Unlock()

	defer func() {
		engine.Lock()
		if _, ret := engine.containers[containerid]; ret {
			delete(engine.containers, containerid)
		}
		engine.Unlock()
	}()

	if ret := engine.useRemovePool(container.Config); ret {
		engine.removePool.Lock()
		if _, ret := engine.removePool.containers[containerid]; !ret {
			engine.removePool.containers[containerid] = &RemoveContainer{
				metaID:      container.MetaID(),
				containerID: containerid,
				timeStamp:   time.Now().Unix(),
				failCount:   0,
			}
			logger.INFO("[#cluster#] engine %s container %s add to remove-delay pool.", engine.IP, ShortContainerID(containerid))
		}
		engine.removePool.Unlock()
	} else {
		if err := engine.client.RemoveContainerRequest(context.Background(), containerid); err != nil {
			return err
		}
		logger.INFO("[#cluster#] engine %s remove container %s", engine.IP, ShortContainerID(containerid))
	}
	return nil
}

// OperateContainer is exported
// Engine operate a container.
func (engine *Engine) OperateContainer(operate models.ContainerOperate) error {

	container := engine.Container(operate.Container)
	if container == nil {
		return fmt.Errorf("operate container %s not found", ShortContainerID(operate.Container))
	}

	if err := engine.client.OperateContainerRequest(context.Background(), operate); err != nil {
		return err
	}

	logger.INFO("[#cluster#] engine %s %s container %s", engine.IP, operate.Action, ShortContainerID(operate.Container))
	if containers, err := engine.updateContainer(operate.Container, engine.containers); err == nil {
		engine.Lock()
		engine.containers = containers
		engine.Unlock()
	}
	return nil
}

// ForceUpgradeContainer is exported
// Engine force upgrade a container.
func (engine *Engine) ForceUpgradeContainer(operate models.ContainerOperate) (*Container, error) {

	container := engine.Container(operate.Container)
	if container == nil {
		return nil, fmt.Errorf("upgrade container %s not found", ShortContainerID(operate.Container))
	}

	tagIndex := strings.LastIndex(container.Config.Image, ":")
	if tagIndex <= 0 {
		return nil, fmt.Errorf("upgrade container %s original tag invalid.", ShortContainerID(operate.Container))
	}

	metaData := engine.configCache.GetMetaData(container.MetaID())
	containerConfig := metaData.MetaBase.Config
	containerConfig.Name = container.Config.Name
	containerConfig.Image = container.Config.Image[0:tagIndex] + ":" + operate.ImageTag
	containerConfig.Env = container.BaseConfig.Env
	if err := engine.RemoveContainer(operate.Container); err != nil {
		logger.WARN("[#cluster#] engine %s upgrading, remove original container %s failure.", engine.IP, ShortContainerID(operate.Container))
	}

	newContainer, err := engine.CreateContainer(containerConfig)
	if err != nil {
		return nil, fmt.Errorf("upgrade create new container %s failure. %s", containerConfig.Image, err)
	}
	return newContainer, nil
}

// UpgradeContainer is exported
// Engine upgrade a container.
func (engine *Engine) UpgradeContainer(operate models.ContainerOperate) (*Container, error) {

	validBaseConfig := false
	baseConfig := ContainerBaseConfig{}
	engine.RLock()
	if container, ret := engine.containers[operate.Container]; ret {
		if container.BaseConfig != nil {
			baseConfig = *container.BaseConfig
			validBaseConfig = true
		}
	}
	engine.RUnlock()

	if !validBaseConfig {
		return nil, fmt.Errorf("engine %s upgrade container %s baseconfig invalid.", engine.IP, ShortContainerID(operate.Container))
	}

	upgradeContainerResponse, err := engine.client.UpgradeContainerRequest(context.Background(), operate)
	if err != nil {
		return nil, fmt.Errorf("engine %s upgrade container %s failure, %s", engine.IP, ShortContainerID(operate.Container), err)
	}

	baseConfig.ID = upgradeContainerResponse.ID
	imageNameSplit := strings.SplitN(baseConfig.Image, ":", 2)
	baseConfig.Image = imageNameSplit[0] + ":" + operate.ImageTag
	engine.configCache.CreateContainerBaseConfig(baseConfig.MetaData.MetaID, &baseConfig)
	engine.configCache.RemoveContainerBaseConfig(baseConfig.MetaData.MetaID, operate.Container)
	logger.INFO("[#cluster#] engine %s %s container %s to %s", engine.IP, operate.Action, ShortContainerID(operate.Container), ShortContainerID(upgradeContainerResponse.ID))
	containers, err := engine.updateContainer(upgradeContainerResponse.ID, engine.containers)
	if err != nil {
		return nil, err
	}

	engine.Lock()
	engine.containers = containers
	container, ret := engine.containers[upgradeContainerResponse.ID]
	if !ret {
		engine.Unlock()
		return nil, fmt.Errorf("upgrade container, update container info failure")
	}
	engine.Unlock()
	return container, nil
}

// RefreshContainers is exported
// Engine refresh all containers.
func (engine *Engine) RefreshContainers() error {

	containers, err := engine.client.GetContainersRequest(context.Background())
	if err != nil {
		return fmt.Errorf("engine %s refresh containers error, %s", engine.IP, err)
	}

	//logger.INFO("[#cluster#] engine %s %s refresh containers.", engine.IP, engine.State())
	merged := make(map[string]*Container)
	for _, container := range containers {
		mergedUpdate, err := engine.updateContainer(container.ID, merged)
		if err != nil {
			logger.ERROR("[#cluster#] %s", err.Error())
		} else {
			merged = mergedUpdate
		}
	}

	engine.Lock()
	engine.containers = merged
	engine.Unlock()
	return nil
}

// ValidateContainers is exported
// clear cluster engine invalid containers
func (engine *Engine) ValidateContainers() {

	expelContainers := Containers{}
	containers := engine.Containers("")
	for _, container := range containers {
		if !container.ValidateConfig() { // get all invalid containers
			containers, err := engine.updateContainer(container.Info.ID, engine.containers)
			if err != nil {
				if strings.Index(err.Error(), "No such container") != -1 {
					expelContainers = append(expelContainers, container)
				}
				continue
			}
			engine.Lock()
			engine.containers = containers
			engine.Unlock()
			if !container.ValidateConfig() {
				expelContainers = append(expelContainers, container)
			}
		}
	}

	// remove all local invalid containers
	for _, container := range expelContainers {
		if err := engine.RemoveContainer(container.Info.ID); err != nil {
			logger.WARN("[#cluster#] engine %s container %s expel, remove error %s.", engine.IP, ShortContainerID(container.Info.ID), err.Error())
			continue
		}
		logger.WARN("[#cluster#] engine %s container %s expel, remove.", engine.IP, ShortContainerID(container.Info.ID))
	}
}

// refreshContainersLoop is exported
// Get container information regularly and engine performance collection
func (engine *Engine) refreshContainersLoop() {

	//refresh loop current time seed.
	seedAt := time.Now()
	//engine performance collection interval
	const perfUpdateInterval = 5 * time.Minute
	lastPrefUpdateAt := seedAt
	//engine validate containers interval
	const doValidateInterval = 15 * time.Minute
	lastValidateAt := seedAt

	for {
		runTicker := time.NewTicker(refreshInterval)
		select {
		case <-runTicker.C:
			{
				runTicker.Stop()
				if engine.IsHealthy() {
					currentAt := time.Now()
					if time.Since(lastPrefUpdateAt) > perfUpdateInterval {
						//engine.updatePerformance()
						lastPrefUpdateAt = currentAt
					}
					if err := engine.RefreshContainers(); err != nil {
						logger.ERROR("[#cluster#] engine %s refresh containers error:%s", engine.IP, err.Error())
					}
					if time.Since(lastValidateAt) > doValidateInterval {
						engine.ValidateContainers()
						lastValidateAt = currentAt
					}
				}
			}
		case <-engine.stopCh:
			{
				runTicker.Stop()
				return
			}
		}
	}
}

// removeContainersDelayLoop is exported
func (engine *Engine) removeContainersDelayLoop() {

	for {
		runTicker := time.NewTicker(delayRemoveInterval)
		select {
		case <-runTicker.C:
			{
				runTicker.Stop()
				seedAt := time.Now()
				engine.removePool.Lock()
				for containerid, removeContainer := range engine.removePool.containers {
					if seedAt.Sub(time.Unix(removeContainer.timeStamp, 0)) >= engine.removePool.removeDelay {
						if removeContainer.failCount < maxRemoveFailThreshold {
							if err := engine.client.RemoveContainerRequest(context.Background(), containerid); err != nil {
								removeContainer.failCount = removeContainer.failCount + 1
								logger.ERROR("[#cluster#] engine %s remove-delay container error, %s", engine.IP, err)
								continue
							}
							delete(engine.removePool.containers, containerid)
							logger.INFO("[#cluster#] engine %s remove-delay container %s", engine.IP, ShortContainerID(containerid))
						} else {
							removeContainer.failCount = 0
							removeContainer.timeStamp = seedAt.Unix()
						}
					}
				}
				engine.removePool.Unlock()
			}
		case <-engine.stopCh:
			{
				runTicker.Stop()
				return
			}
		}
	}
}

// useRemovePool is exported
// remove a container use pool.
func (engine *Engine) useRemovePool(containerConfig *ContainerConfig) bool {

	if engine.removePool.removeDelay == time.Duration(0) {
		return false
	}

	if containerConfig.NetworkMode != "bridge" && containerConfig.NetworkMode != "nat" {
		return false
	}

	configEnvMap := convert.ConvertKVStringSliceToMap(containerConfig.Env)
	metaData := engine.configCache.GetMetaData(configEnvMap["HUMPBACK_CLUSTER_METAID"])
	if metaData == nil {
		return false
	}

	for _, portBindings := range metaData.Config.Ports {
		if portBindings.PublicPort != 0 {
			return false
		}
	}
	return metaData.IsRemoveDelay
}

// updateSpecs exported
func (engine *Engine) updateSpecs() error {

	dockerInfo, err := engine.client.GetDockerInfoRequest(context.Background())
	if err != nil {
		return fmt.Errorf("engine %s update specs error, %s", engine.IP, err)
	}

	//logger.INFO("[#cluster#] engine %s update specs.", engine.IP)
	engine.Lock()
	engine.ID = dockerInfo.ID
	engine.Name = strings.ToUpper(dockerInfo.Name)
	engine.Cpus = int64(dockerInfo.NCPU)
	engine.Memory = int64(math.Ceil(float64(dockerInfo.MemTotal) / 1024.0 / 1024.0))
	if dockerInfo.Driver != "" {
		engine.Labels["storagedirver"] = dockerInfo.Driver
	}

	if dockerInfo.KernelVersion != "" {
		engine.Labels["kernelversion"] = dockerInfo.KernelVersion
	}

	if dockerInfo.OperatingSystem != "" {
		engine.Labels["operatingsystem"] = dockerInfo.OperatingSystem
	}

	for _, label := range dockerInfo.Labels {
		kv := strings.SplitN(label, "=", 2)
		if len(kv) != 2 {
			continue
		}
		engine.Labels[kv[0]] = kv[1]
	}
	engine.Unlock()
	return nil
}

// updateContainer exported
func (engine *Engine) updateContainer(containerid string, containers map[string]*Container) (map[string]*Container, error) {

	var container *Container
	engine.RLock()
	if current, ret := engine.containers[containerid]; ret {
		container = current
	} else {
		container = &Container{
			Engine: engine,
		}
	}
	engine.RUnlock()

	containerJSON, err := engine.client.GetContainerRequest(context.Background(), containerid)
	if err != nil {
		return nil, fmt.Errorf("update container error, %s", err)
	}

	engine.Lock()
	container.update(engine, containerJSON)
	containers[containerid] = container
	engine.Unlock()
	return containers, nil
}
