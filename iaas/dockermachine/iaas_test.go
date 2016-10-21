// Copyright 2016 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dockermachine

import (
	"fmt"

	"github.com/tsuru/config"
	"github.com/tsuru/tsuru/iaas"
	check "gopkg.in/check.v1"
)

func (s *S) TestBuildDriverOpts(c *check.C) {
	config.Set("iaas:dockermachine:driver:options", map[interface{}]interface{}{
		"options1": 1,
		"options2": "2",
		"options3": "3",
	})
	defer config.Unset("iaas:dockermachine:driver:options")
	dm := newDockerMachineIaaS("dockermachine")
	driverOpts := dm.(*dockerMachineIaaS).buildDriverOpts(map[string]string{
		"options2": "new2",
	})
	c.Assert(driverOpts["options1"], check.Equals, 1)
	c.Assert(driverOpts["options2"], check.Equals, "new2")
	c.Assert(driverOpts["options3"], check.Equals, "3")
}

func (s *S) TestCreateMachineIaaS(c *check.C) {
	config.Set("iaas:dockermachine:ca-path", "/etc/ca-path")
	defer config.Unset("iaas:dockermachine:ca-path")
	i := newDockerMachineIaaS("dockermachine")
	dmIaas := i.(*dockerMachineIaaS)
	dmIaas.apiFactory = newFakeDockerMachine
	m, err := dmIaas.CreateMachine(map[string]string{
		"insecure-registry":  "registry.com",
		"name":               "host-name",
		"driver":             "driver-name",
		"docker-install-url": "http://getdocker.com",
	})
	expectedMachine := &iaas.Machine{
		Id: "host-name",
		CreationParams: map[string]string{
			"insecure-registry":  "registry.com",
			"driver":             "driver-name",
			"docker-install-url": "http://getdocker.com",
		},
	}
	c.Assert(err, check.IsNil)
	c.Assert(m, check.DeepEquals, expectedMachine)
	c.Assert(fakeDM.closed, check.Equals, true)
	c.Assert(fakeDM.hostOpts.InsecureRegistry, check.Equals, "registry.com")
	c.Assert(fakeDM.hostOpts.DockerEngineInstallURL, check.Equals, "http://getdocker.com")
}

func (s *S) TestCreateMachineIaaSConfigFromIaaSConfig(c *check.C) {
	config.Set("iaas:dockermachine:docker-install-url", "https://getdocker.com")
	config.Set("iaas:dockermachine:ca-path", "/etc/ca-path")
	config.Set("iaas:dockermachine:driver:name", "driver-name")
	defer config.Unset("iaas:dockermachine:ca-path")
	defer config.Unset("iaas:dockermachine:driver:name")
	defer config.Unset("iaas:dockermachine:docker-install-url")
	i := newDockerMachineIaaS("dockermachine")
	dmIaas := i.(*dockerMachineIaaS)
	dmIaas.apiFactory = newFakeDockerMachine
	m, err := dmIaas.CreateMachine(map[string]string{
		"name": "host-name",
	})
	expectedMachine := &iaas.Machine{
		Id:             "host-name",
		CreationParams: map[string]string{"driver": "driver-name"},
	}
	c.Assert(err, check.IsNil)
	c.Assert(m, check.DeepEquals, expectedMachine)
	c.Assert(fakeDM.hostOpts.DockerEngineInstallURL, check.Equals, "https://getdocker.com")
}

func (s *S) TestCreateMachineIaaSFailsWithNoDriver(c *check.C) {
	config.Unset("iaas:dockermachine:driver")
	config.Set("iaas:dockermachine:ca-path", "/etc/ca-path")
	defer config.Unset("iaas:dockermachine:ca-path")
	i := newDockerMachineIaaS("dockermachine")
	dmIaas := i.(*dockerMachineIaaS)
	dmIaas.apiFactory = newFakeDockerMachine
	m, err := dmIaas.CreateMachine(map[string]string{
		"name": "host-name",
	})
	c.Assert(err, check.Equals, errDriverNotSet)
	c.Assert(m, check.IsNil)
}

func (s *S) TestCreateMachineGeneratesName(c *check.C) {
	machines, err := iaas.ListMachines()
	c.Assert(err, check.IsNil)
	config.Set("iaas:dockermachine:ca-path", "/etc/ca-path")
	defer config.Unset("iaas:dockermachine:ca-path")
	i := newDockerMachineIaaS("dockermachine")
	dmIaas := i.(*dockerMachineIaaS)
	dmIaas.apiFactory = newFakeDockerMachine
	m, err := dmIaas.CreateMachine(map[string]string{
		"pool":   "theonepool",
		"driver": "driver-name",
	})
	c.Assert(err, check.IsNil)
	c.Assert(m.Id, check.Equals, fmt.Sprintf("theonepool-%d", len(machines)+1))
}

func (s *S) TestCreateMachineDeletesMachineWithError(c *check.C) {
	config.Set("iaas:dockermachine:ca-path", "/etc/ca-path")
	defer config.Unset("iaas:dockermachine:ca-path")
	i := newDockerMachineIaaS("dockermachine")
	dmIaas := i.(*dockerMachineIaaS)
	dmIaas.apiFactory = newFakeDockerMachine
	m, err := dmIaas.CreateMachine(map[string]string{
		"pool":   "theonepool",
		"driver": "driver-name",
		"name":   "my-machine",
		"error":  "failed to create",
	})
	c.Assert(err, check.NotNil)
	c.Assert(m, check.IsNil)
	c.Assert(fakeDM.deletedMachine.Id, check.Equals, "my-machine")
}

func (s *S) TestDeleteMachineIaaS(c *check.C) {
	i := newDockerMachineIaaS("dockermachine")
	dmIaas := i.(*dockerMachineIaaS)
	dmIaas.apiFactory = newFakeDockerMachine
	m := &iaas.Machine{Id: "machine-id"}
	err := dmIaas.DeleteMachine(m)
	c.Assert(err, check.IsNil)
	c.Assert(fakeDM.deletedMachine, check.DeepEquals, m)
	c.Assert(fakeDM.closed, check.Equals, true)
}
