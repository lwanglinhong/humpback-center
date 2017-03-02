package response

import "github.com/humpback/humpback-center/models"

/*
ClusterGroupsResponse
Method:  GET
Route:   /v1/cluster/groups
*/
type ClusterGroupsResponse struct {
	Groups []*models.Group `json:"groups"`
}

func NewClusterGroupsResponse(groups []*models.Group) *ClusterGroupsResponse {

	return &ClusterGroupsResponse{
		Groups: groups,
	}
}

/*
ClusterGroupResponse
Method:  GET
Route:   /v1/cluster/groups/{groupid}
*/
type ClusterGroupResponse struct {
	Group *models.Group `json:"group"`
}

func NewClusterGroupResponse(group *models.Group) *ClusterGroupResponse {

	return &ClusterGroupResponse{
		Group: group,
	}
}

/*
ClusterGroupEnginesResponse
Method:  GET
Route:   /v1/cluster/groups/{groupid}/engines
*/
type ClusterGroupEnginesResponse struct {
	Engines []*models.Engine `json:"engines"`
}

func NewClusterGroupEnginesResponse(engines []*models.Engine) *ClusterGroupEnginesResponse {

	return &ClusterGroupEnginesResponse{
		Engines: engines,
	}
}

/*
ClusterEngineResponse
Method:  GET
Route:   /v1/cluster/engines/{engineid}
*/
type ClusterEngineResponse struct {
	Engine *models.Engine `json:"engine"`
}

func NewClusterEngineResponse(engine *models.Engine) *ClusterEngineResponse {

	return &ClusterEngineResponse{
		Engine: engine,
	}
}

/*
ClusterGroupEventResponse
Method:  POST
Route:   /v1/cluster/groups/event
Message: response message
*/
type ClusterGroupEventResponse struct {
	Message string `json:"message"`
}

func NewClusterGroupEventResponse(message string) *ClusterGroupEventResponse {

	return &ClusterGroupEventResponse{
		Message: message,
	}
}

/*
ClusterCreateContainerResponse
Method:  POST
Route:   /v1/cluster/containers
GroupID: cluster groupid
Pairs:   create container for engine ip pair
*/
type ClusterCreateContainerResponse struct {
	GroupID      string                        `json:"groupid"`
	CreateResult string                        `json:"createresult"`
	CreatePairs  []*models.CreateContainerPair `json:"createpairs"`
}

func NewClusterCreateContainerResponse(groupid string, instances int, createparis []*models.CreateContainerPair) *ClusterCreateContainerResponse {

	createresult := "created all"
	if instances > len(createparis) {
		createresult = "created partial"
	}

	return &ClusterCreateContainerResponse{
		GroupID:      groupid,
		CreateResult: createresult,
		CreatePairs:  createparis,
	}
}
