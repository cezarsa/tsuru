// Copyright 2015 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package router

import (
	"github.com/tsuru/tsuru/hc"
)

// BuildHealthCheck creates a healthcheck function for the given routerName.
//
// It will call the HealthCheck() method in the router (only if it's also a
// HealthChecker), for each instance of it (including the "main" instance and
// all custom routers).
func BuildHealthCheck(routerName string) func() error {
	return func() error {
		configRouters, err := listConfigRouters()
		if err != nil {
			return hc.ErrDisabledComponent
		}
		checkCount := 0
		for _, r := range configRouters {
			if r.Name != routerName && r.Type != routerName {
				continue
			}
			checkCount++
			err := healthCheck(r.Name)
			if err != nil {
				return err
			}
		}
		if checkCount == 0 {
			return hc.ErrDisabledComponent
		}
		return nil
	}
}

func healthCheck(name string) error {
	router, err := Get(name)
	if err != nil {
		return err
	}
	if hrouter, ok := router.(HealthChecker); ok {
		return hrouter.HealthCheck()
	}
	return hc.ErrDisabledComponent
}
