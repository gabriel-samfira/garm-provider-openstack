package provider

import (
	"context"
	"fmt"

	"github.com/cloudbase/garm-provider-openstack/client"
	"github.com/cloudbase/garm-provider-openstack/config"

	"github.com/cloudbase/garm/params"
	"github.com/cloudbase/garm/runner/providers/common"
	"github.com/cloudbase/garm/runner/providers/external/execution"
)

var _ execution.ExternalProvider = &openstackProvider{}

const (
	controllerIDTagName = "garm-controller-id"
	poolIDTagName       = "garm-pool-id"
)

var statusMap = map[string]string{
	"ACTIVE":   "running",
	"SHUTOFF":  "stopped",
	"BUILD":    "pending_create",
	"ERROR":    "error",
	"DELETING": "pending_delete",
}

var addrTypeMap = map[string]params.AddressType{
	"fixed":    params.PrivateAddress,
	"floating": params.PublicAddress,
}

func NewOpenStackProvider(configPath, controllerID string) (execution.ExternalProvider, error) {
	conf, err := config.NewConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}

	cli, err := client.NewClient(conf, controllerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get client: %w", err)
	}
	return &openstackProvider{
		cfg:          conf,
		controllerID: controllerID,
		cli:          cli,
	}, nil
}

type openstackProvider struct {
	cfg          *config.Config
	cli          *client.OpenstackClient
	controllerID string
}

func openstackServerToInstance(srv client.ServerWithExt) params.Instance {
	addresses := []params.Address{}
	for _, val := range srv.Addresses {
		addrs, ok := val.([]interface{})
		if !ok {
			continue
		}
		for _, addr := range addrs {
			addrDetails, ok := addr.(map[string]interface{})
			if !ok {
				continue
			}
			address := addrDetails["addr"]
			addrType := addrDetails["OS-EXT-IPS:type"]
			if addrType == nil || address == nil {
				continue
			}
			addrAsStr, ok := address.(string)
			if !ok {
				continue
			}
			addrTypeAsStr, ok := addrType.(string)
			if !ok {
				continue
			}

			if addrTypeAsStr != "fixed" && addrTypeAsStr != "floating" {
				continue
			}
			addresses = append(addresses, params.Address{
				Address: addrAsStr,
				Type:    addrTypeMap[addrTypeAsStr],
			})
		}
	}

	arch := srv.Metadata["os_arch"]
	osType := srv.Metadata["os_type"]
	osName := srv.Metadata["os_name"]
	osVersion := srv.Metadata["os_version"]
	status := statusMap[srv.Status]
	instance := params.Instance{
		ProviderID: srv.ID,
		Name:       srv.Name,
		OSArch:     params.OSArch(arch),
		OSType:     params.OSType(osType),
		Status:     common.InstanceStatus(status),
		OSName:     osName,
		OSVersion:  osVersion,
		Addresses:  addresses,
	}

	return instance
}

// CreateInstance creates a new compute instance in the provider.
func (a *openstackProvider) CreateInstance(ctx context.Context, bootstrapParams params.BootstrapInstance) (params.Instance, error) {
	spec, err := NewMachineSpec(bootstrapParams, a.cfg, a.controllerID)
	if err != nil {
		return params.Instance{}, fmt.Errorf("failed to build machine spec: %w", err)
	}
	flavor, err := a.cli.GetFlavor(spec.Flavor)
	if err != nil {
		return params.Instance{}, fmt.Errorf("failed to resolve flavor %s: %w", bootstrapParams.Flavor, err)
	}

	net, err := a.cli.GetNetwork(spec.NetworkID)
	if err != nil {
		return params.Instance{}, fmt.Errorf("failed to resolve network %s: %w", spec.NetworkID, err)
	}

	image, err := a.cli.GetImage(spec.Image)
	if err != nil {
		return params.Instance{}, fmt.Errorf("failed to resolve image info: %w", err)
	}
	spec.SetSpecFromImage(*image)

	srvCreateOpts, err := spec.GetServerCreateOpts(*flavor, *net, *image)
	if err != nil {
		return params.Instance{}, fmt.Errorf("failed to get server create options: %w", err)
	}

	var srv client.ServerWithExt
	if !spec.BootFromVolume {
		srv, err = a.cli.CreateServerFromImage(srvCreateOpts)
		if err != nil {
			return params.Instance{}, fmt.Errorf("failed to create server: %w", err)
		}
	} else {
		createOption, err := spec.GetBootFromVolumeOpts(srvCreateOpts)
		if err != nil {
			return params.Instance{}, fmt.Errorf("failed to get boot from volume create options: %w", err)
		}
		srv, err = a.cli.CreateServerFromVolume(createOption, spec.BootstrapParams.Name)
		if err != nil {
			return params.Instance{}, fmt.Errorf("failed to create server: %w", err)
		}
	}
	return openstackServerToInstance(srv), nil
}

// Delete instance will delete the instance in a provider.
func (a *openstackProvider) DeleteInstance(ctx context.Context, instance string) error {
	if err := a.cli.DeleteServer(instance, true); err != nil {
		return fmt.Errorf("failed to delete server: %w", err)
	}
	return nil
}

// GetInstance will return details about one instance.
func (a *openstackProvider) GetInstance(ctx context.Context, instance string) (params.Instance, error) {
	srv, err := a.cli.GetServer(instance)
	if err != nil {
		return params.Instance{}, fmt.Errorf("failed to get server: %w", err)
	}
	return openstackServerToInstance(srv), nil
}

// ListInstances will list all instances for a provider.
func (a *openstackProvider) ListInstances(ctx context.Context, poolID string) ([]params.Instance, error) {
	servers, err := a.cli.ListServers(poolID)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}

	ret := make([]params.Instance, len(servers))
	for idx, srv := range servers {
		ret[idx] = openstackServerToInstance(srv)
	}
	return ret, nil
}

// RemoveAllInstances will remove all instances created by this provider.
func (a *openstackProvider) RemoveAllInstances(ctx context.Context) error {
	return nil
}

// Stop shuts down the instance.
func (a *openstackProvider) Stop(ctx context.Context, instance string, force bool) error {
	if err := a.cli.StopServer(instance); err != nil {
		return fmt.Errorf("failed to stop server: %w", err)
	}
	return nil
}

// Start boots up an instance.
func (a *openstackProvider) Start(ctx context.Context, instance string) error {
	if err := a.cli.StartServer(instance); err != nil {
		return fmt.Errorf("failed to stop server: %w", err)
	}
	return nil
}
