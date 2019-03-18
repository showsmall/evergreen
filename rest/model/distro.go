package model

import (
	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/cloud"
	"github.com/evergreen-ci/evergreen/model/distro"
	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
)

// APIPlannerSettings is the model to be returned by the API whenever distro.PlannerSettings are fetched
type APIPlannerSettings struct {
	Version                APIString   `json:"version"`
	MinimumHosts           int         `json:"minimum_hosts"`
	MaximumHosts           int         `json:"maximum_hosts"`
	TargetTime             APIDuration `json:"target_time"`
	AcceptableHostIdleTime APIDuration `json:"acceptable_host_idle_time"`
	GroupVersions          bool        `json:"group_versions"`
	PatchZipperFactor      int         `json:"patch_zipper_factor"`
	MainlineFirst          bool        `json:"mainline_first"`
	PatchFirst             bool        `json:"patch_first"`
}

// BuildFromService converts from service level distro.PlannerSetting to an APIPlannerSettings
func (s *APIPlannerSettings) BuildFromService(h interface{}) error {
	var settings distro.PlannerSettings
	switch v := h.(type) {
	case distro.PlannerSettings:
		settings = v
	case *distro.PlannerSettings:
		settings = *v
	default:
		return errors.Errorf("%T is not an supported expansion type", h)
	}

	if len(settings.Version) == 0 {
		s.Version = ToAPIString(evergreen.PlannerVersionLegacy)
	} else {
		s.Version = ToAPIString(settings.Version)
	}
	s.MinimumHosts = settings.MinimumHosts
	s.MaximumHosts = settings.MaximumHosts
	s.TargetTime = NewAPIDuration(settings.TargetTime)
	s.AcceptableHostIdleTime = NewAPIDuration(settings.AcceptableHostIdleTime)
	s.GroupVersions = settings.GroupVersions
	s.PatchZipperFactor = settings.PatchZipperFactor
	s.MainlineFirst = settings.MainlineFirst
	s.PatchFirst = settings.PatchFirst

	return nil
}

// ToService returns a service layer distro.PlannerSettings using the data from APIPlannerSettings
func (s *APIPlannerSettings) ToService() (interface{}, error) {
	settings := distro.PlannerSettings{}
	settings.Version = FromAPIString(s.Version)
	if len(settings.Version) == 0 {
		settings.Version = evergreen.PlannerVersionLegacy
	}
	settings.Version = FromAPIString(s.Version)
	settings.MinimumHosts = s.MinimumHosts
	settings.MaximumHosts = s.MaximumHosts
	settings.TargetTime = s.TargetTime.ToDuration()
	settings.AcceptableHostIdleTime = s.AcceptableHostIdleTime.ToDuration()
	settings.GroupVersions = s.GroupVersions
	settings.PatchZipperFactor = s.PatchZipperFactor
	settings.MainlineFirst = s.MainlineFirst
	settings.PatchFirst = s.PatchFirst

	return interface{}(settings), nil
}

// APIDistro is the model to be returned by the API whenever distros are fetched
type APIDistro struct {
	Name             APIString              `json:"name"`
	UserSpawnAllowed bool                   `json:"user_spawn_allowed"`
	Provider         APIString              `json:"provider"`
	ProviderSettings map[string]interface{} `json:"settings"`
	ImageID          APIString              `json:"image_id"`
	Arch             APIString              `json:"arch"`
	WorkDir          APIString              `json:"work_dir"`
	PoolSize         int                    `json:"pool_size"`
	SetupAsSudo      bool                   `json:"setup_as_sudo"`
	Setup            APIString              `json:"setup"`
	Teardown         APIString              `json:"teardown"`
	User             APIString              `json:"user"`
	SSHKey           APIString              `json:"ssh_key"`
	SSHOptions       []string               `json:"ssh_options"`
	Expansions       []APIExpansion         `json:"expansions"`
	Disabled         bool                   `json:"disabled"`
	ContainerPool    APIString              `json:"container_pool"`
	PlannerSettings  APIPlannerSettings     `json:"planner_settings"`
}

// BuildFromService converts from service level distro.Distro to an APIDistro
func (apiDistro *APIDistro) BuildFromService(h interface{}) error {
	var d distro.Distro
	switch v := h.(type) {
	case distro.Distro:
		d = v
	case *distro.Distro:
		d = *v
	default:
		return errors.Errorf("%T is not an supported expansion type", h)
	}

	apiDistro.Name = ToAPIString(d.Id)
	apiDistro.UserSpawnAllowed = d.SpawnAllowed
	apiDistro.Provider = ToAPIString(d.Provider)
	if d.ProviderSettings != nil && (d.Provider == evergreen.ProviderNameEc2Auto || d.Provider == evergreen.ProviderNameEc2OnDemand || d.Provider == evergreen.ProviderNameEc2Spot) {
		ec2Settings := &cloud.EC2ProviderSettings{}
		err := mapstructure.Decode(d.ProviderSettings, ec2Settings)
		if err != nil {
			return err
		}
		apiDistro.ImageID = ToAPIString(ec2Settings.AMI)
	}
	if d.ProviderSettings != nil {
		apiDistro.ProviderSettings = *d.ProviderSettings
	}
	apiDistro.Arch = ToAPIString(d.Arch)
	apiDistro.WorkDir = ToAPIString(d.WorkDir)
	apiDistro.PoolSize = d.PoolSize
	apiDistro.SetupAsSudo = d.SetupAsSudo
	apiDistro.Setup = ToAPIString(d.Setup)
	apiDistro.Teardown = ToAPIString(d.Teardown)
	apiDistro.User = ToAPIString(d.User)
	apiDistro.SSHKey = ToAPIString(d.SSHKey)
	apiDistro.Disabled = d.Disabled
	apiDistro.ContainerPool = ToAPIString(d.ContainerPool)
	apiDistro.SSHOptions = d.SSHOptions
	if d.Expansions != nil {
		apiDistro.Expansions = []APIExpansion{}
		for _, e := range d.Expansions {
			expansion := APIExpansion{}
			if err := expansion.BuildFromService(e); err != nil {
				return errors.Wrap(err, "Error converting from distro.Expansion to model.APIExpansion")
			}
			apiDistro.Expansions = append(apiDistro.Expansions, expansion)
		}
	}
	settings := APIPlannerSettings{}
	if err := settings.BuildFromService(d.PlannerSettings); err != nil {
		return errors.Wrap(err, "Error converting from distro.PlannerSettings to model.APIPlannerSettings")
	}
	apiDistro.PlannerSettings = settings

	return nil
}

// ToService returns a service layer distro using the data from APIDistro
func (apiDistro *APIDistro) ToService() (interface{}, error) {
	d := distro.Distro{}
	d.Id = FromAPIString(apiDistro.Name)
	d.Arch = FromAPIString(apiDistro.Arch)
	d.WorkDir = FromAPIString(apiDistro.WorkDir)
	d.PoolSize = apiDistro.PoolSize
	d.Provider = FromAPIString(apiDistro.Provider)
	if apiDistro.ProviderSettings != nil {
		d.ProviderSettings = &apiDistro.ProviderSettings
	}
	d.SetupAsSudo = apiDistro.SetupAsSudo
	d.Setup = FromAPIString(apiDistro.Setup)
	d.Teardown = FromAPIString(apiDistro.Teardown)
	d.User = FromAPIString(apiDistro.User)
	d.SSHKey = FromAPIString(apiDistro.SSHKey)
	d.SSHOptions = apiDistro.SSHOptions
	d.SpawnAllowed = apiDistro.UserSpawnAllowed
	d.Expansions = []distro.Expansion{}
	for _, e := range apiDistro.Expansions {
		i, err := e.ToService()
		if err != nil {
			return nil, errors.Wrap(err, "Error converting from model.APIExpansion to distro.Expansion")
		}
		expansion, ok := i.(distro.Expansion)
		if !ok {
			return nil, errors.Errorf("Unexpected type %T for distro.Expansion", i)
		}
		d.Expansions = append(d.Expansions, expansion)
	}
	d.Disabled = apiDistro.Disabled
	d.ContainerPool = FromAPIString(apiDistro.ContainerPool)
	i, err := apiDistro.PlannerSettings.ToService()
	if err != nil {
		return nil, errors.Wrap(err, "Error converting from model.APIPlannerSettings to distro.PlannerSetting")
	}
	settings, ok := i.(distro.PlannerSettings)
	if !ok {
		return nil, errors.Errorf("Unexpected type %T for distro.PlannerSettings", i)
	}
	d.PlannerSettings = settings

	return &d, nil
}

// APIExpansion is derived from a service layer distro.Expansion
type APIExpansion struct {
	Key   APIString `json:"key"`
	Value APIString `json:"value"`
}

// BuildFromService converts a service level distro.Expansion to an APIExpansion
func (e *APIExpansion) BuildFromService(h interface{}) error {
	switch val := h.(type) {
	case distro.Expansion:
		e.Key = ToAPIString(val.Key)
		e.Value = ToAPIString(val.Value)
	default:
		return errors.Errorf("%T is not an supported expansion type", h)
	}
	return nil
}

// ToService returns a service layer distro.Expansion using the data from an APIExpansion
func (e *APIExpansion) ToService() (interface{}, error) {
	d := distro.Expansion{}
	d.Key = FromAPIString(e.Key)
	d.Value = FromAPIString(e.Value)

	return interface{}(d), nil
}
