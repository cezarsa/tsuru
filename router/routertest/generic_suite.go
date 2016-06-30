// Copyright 2016 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package routertest

import (
	"fmt"
	"net/url"
	"sort"

	"github.com/tsuru/config"
	"github.com/tsuru/tsuru/db"
	"github.com/tsuru/tsuru/router"
	"gopkg.in/check.v1"
)

type urlHostChecker struct {
	check.CheckerInfo
}

func (c *urlHostChecker) Check(params []interface{}, names []string) (bool, string) {
	if len(params) != 2 {
		return false, "expected 2 params"
	}
	vals := make([][]*url.URL, 2)
	for i, p := range params {
		switch v := p.(type) {
		case *url.URL:
			vals[i] = []*url.URL{v}
		case url.URL:
			vals[i] = []*url.URL{&v}
		case []*url.URL:
			vals[i] = v
		}
		for j := range vals[i] {
			vals[i][j] = &url.URL{Host: vals[i][j].Host}
		}
	}
	return check.DeepEquals.Check([]interface{}{vals[0], vals[1]}, names)
}

var HostEquals check.Checker = &urlHostChecker{
	check.CheckerInfo{Name: "HostEquals", Params: []string{"obtained", "expected"}},
}

type RouterSuite struct {
	Router            router.Router
	SetUpSuiteFunc    func(c *check.C)
	SetUpTestFunc     func(c *check.C)
	TearDownSuiteFunc func(c *check.C)
	TearDownTestFunc  func(c *check.C)
}

func (s *RouterSuite) SetUpSuite(c *check.C) {
	if s.SetUpSuiteFunc != nil {
		s.SetUpSuiteFunc(c)
	}
}

func (s *RouterSuite) SetUpTest(c *check.C) {
	if s.SetUpTestFunc != nil {
		s.SetUpTestFunc(c)
	}
	c.Logf("generic router test for %T", s.Router)
}

func (s *RouterSuite) TearDownSuite(c *check.C) {
	if s.TearDownSuiteFunc != nil {
		s.TearDownSuiteFunc(c)
	}
	if _, err := config.GetString("database:name"); err == nil {
		conn, err := db.Conn()
		c.Assert(err, check.IsNil)
		defer conn.Close()
		conn.Apps().Database.DropDatabase()
	}
}

func (s *RouterSuite) TearDownTest(c *check.C) {
	if s.TearDownTestFunc != nil {
		s.TearDownTestFunc(c)
	}
}

func (s *RouterSuite) TestRouteAddBackendAndRoute(c *check.C) {
	name := "backend1"
	err := s.Router.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(name, addr)
	c.Assert(err, check.IsNil)
	routes, err := s.Router.Routes(name)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{addr})
	err = s.Router.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestRouteAddBackendOptsAndRoute(c *check.C) {
	optsRouter, ok := s.Router.(router.OptsRouter)
	if !ok {
		c.Skip(fmt.Sprintf("%T does not implement OptsRouter", s.Router))
	}
	name := "backend1"
	err := optsRouter.AddBackendOpts(name, map[string]string{})
	c.Assert(err, check.IsNil)
	err = optsRouter.AddBackendOpts(name, nil)
	c.Assert(err, check.Equals, router.ErrBackendExists)
	addr, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(name, addr)
	c.Assert(err, check.IsNil)
	routes, err := s.Router.Routes(name)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{addr})
	err = s.Router.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestRouteRemoveRouteAndBackend(c *check.C) {
	name := "backend1"
	err := s.Router.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(name, addr1)
	addr2, err := url.Parse("http://10.10.10.11:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(name, addr2)
	c.Assert(err, check.IsNil)
	err = s.Router.RemoveRoute(name, addr1)
	c.Assert(err, check.IsNil)
	routes, err := s.Router.Routes(name)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{addr2})
	err = s.Router.RemoveRoute(name, addr2)
	c.Assert(err, check.IsNil)
	routes, err = s.Router.Routes(name)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{})
	err = s.Router.RemoveBackend(name)
	c.Assert(err, check.IsNil)
	_, err = s.Router.Routes(name)
	c.Assert(err, check.Equals, router.ErrBackendNotFound)
}

func (s *RouterSuite) TestRouteRemoveUnknownRoute(c *check.C) {
	name := "backend1"
	err := s.Router.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.RemoveRoute(name, addr1)
	c.Assert(err, check.Equals, router.ErrRouteNotFound)
	err = s.Router.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestRouteAddDupBackend(c *check.C) {
	name := "backend1"
	err := s.Router.AddBackend(name)
	c.Assert(err, check.IsNil)
	err = s.Router.AddBackend(name)
	c.Assert(err, check.Equals, router.ErrBackendExists)
	err = s.Router.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestRouteAddDupRoute(c *check.C) {
	name := "backend1"
	err := s.Router.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(name, addr1)
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(name, addr1)
	c.Assert(err, check.Equals, router.ErrRouteExists)
	err = s.Router.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestRouteAddRouteInvalidBackend(c *check.C) {
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute("backend1", addr1)
	c.Assert(err, check.Equals, router.ErrBackendNotFound)
}

type URLList []*url.URL

func (l URLList) Len() int           { return len(l) }
func (l URLList) Swap(i, j int)      { l[i], l[j] = l[j], l[i] }
func (l URLList) Less(i, j int) bool { return l[i].String() < l[j].String() }

func (s *RouterSuite) TestRouteAddRoutes(c *check.C) {
	name := "backend1"
	err := s.Router.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	addr2, err := url.Parse("http://10.10.10.11:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoutes(name, []*url.URL{addr1, addr2})
	c.Assert(err, check.IsNil)
	routes, err := s.Router.Routes(name)
	c.Assert(err, check.IsNil)
	sort.Sort(URLList(routes))
	c.Assert(routes, HostEquals, []*url.URL{addr1, addr2})
	err = s.Router.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestRouteAddRoutesIgnoreRepeated(c *check.C) {
	name := "backend1"
	err := s.Router.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(name, addr1)
	c.Assert(err, check.IsNil)
	addr2, err := url.Parse("http://10.10.10.11:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoutes(name, []*url.URL{addr1, addr2})
	c.Assert(err, check.IsNil)
	routes, err := s.Router.Routes(name)
	c.Assert(err, check.IsNil)
	sort.Sort(URLList(routes))
	c.Assert(routes, HostEquals, []*url.URL{addr1, addr2})
	err = s.Router.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestRouteRemoveRoutes(c *check.C) {
	name := "backend1"
	err := s.Router.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	addr2, err := url.Parse("http://10.10.10.11:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoutes(name, []*url.URL{addr1, addr2})
	c.Assert(err, check.IsNil)
	routes, err := s.Router.Routes(name)
	c.Assert(err, check.IsNil)
	sort.Sort(URLList(routes))
	c.Assert(routes, HostEquals, []*url.URL{addr1, addr2})
	err = s.Router.RemoveRoutes(name, []*url.URL{addr1, addr2})
	c.Assert(err, check.IsNil)
	routes, err = s.Router.Routes(name)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{})
	err = s.Router.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestRouteRemoveRoutesIgnoreNonExisting(c *check.C) {
	name := "backend1"
	err := s.Router.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	addr2, err := url.Parse("http://10.10.10.11:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoutes(name, []*url.URL{addr1, addr2})
	c.Assert(err, check.IsNil)
	routes, err := s.Router.Routes(name)
	c.Assert(err, check.IsNil)
	sort.Sort(URLList(routes))
	c.Assert(routes, HostEquals, []*url.URL{addr1, addr2})
	addr3, err := url.Parse("http://10.10.10.12:8080")
	c.Assert(err, check.IsNil)
	err = s.Router.RemoveRoutes(name, []*url.URL{addr1, addr3, addr2})
	c.Assert(err, check.IsNil)
	routes, err = s.Router.Routes(name)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{})
	err = s.Router.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestSwap(c *check.C) {
	backend1 := "mybackend1"
	backend2 := "mybackend2"
	addr1, _ := url.Parse("http://127.0.0.1:8080")
	addr2, _ := url.Parse("http://10.10.10.10:8080")
	err := s.Router.AddBackend(backend1)
	c.Assert(err, check.IsNil)
	backend1OrigAddr, err := s.Router.Addr(backend1)
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(backend1, addr1)
	c.Assert(err, check.IsNil)
	err = s.Router.AddBackend(backend2)
	c.Assert(err, check.IsNil)
	backend2OrigAddr, err := s.Router.Addr(backend2)
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(backend2, addr2)
	c.Assert(err, check.IsNil)
	err = s.Router.Swap(backend1, backend2, false)
	c.Assert(err, check.IsNil)
	backAddr1, err := s.Router.Addr(backend1)
	c.Assert(err, check.IsNil)
	c.Assert(backAddr1, check.Equals, backend2OrigAddr)
	backAddr2, err := s.Router.Addr(backend2)
	c.Assert(err, check.IsNil)
	c.Assert(backAddr2, check.Equals, backend1OrigAddr)
	routes, err := s.Router.Routes(backend1)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{addr1})
	routes, err = s.Router.Routes(backend2)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{addr2})
	addr3, _ := url.Parse("http://127.0.0.2:8080")
	addr4, _ := url.Parse("http://10.10.10.11:8080")
	err = s.Router.AddRoute(backend1, addr3)
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(backend2, addr4)
	c.Assert(err, check.IsNil)
	routes, err = s.Router.Routes(backend1)
	c.Assert(err, check.IsNil)
	c.Assert(routes, check.HasLen, 2)
	routesStrs := []string{routes[0].Host, routes[1].Host}
	sort.Strings(routesStrs)
	c.Assert(routesStrs, check.DeepEquals, []string{addr1.Host, addr3.Host})
	routes, err = s.Router.Routes(backend2)
	c.Assert(err, check.IsNil)
	c.Assert(routes, check.HasLen, 2)
	routesStrs = []string{routes[0].Host, routes[1].Host}
	sort.Strings(routesStrs)
	c.Assert(routesStrs, check.DeepEquals, []string{addr2.Host, addr4.Host})
	err = s.Router.Swap(backend1, backend2, false)
	c.Assert(err, check.IsNil)
	err = s.Router.RemoveBackend(backend1)
	c.Assert(err, check.IsNil)
	err = s.Router.RemoveBackend(backend2)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestSwapTwice(c *check.C) {
	backend1 := "mybackend1"
	backend2 := "mybackend2"
	addr1, _ := url.Parse("http://127.0.0.1:8080")
	addr2, _ := url.Parse("http://10.10.10.10:8080")
	err := s.Router.AddBackend(backend1)
	c.Assert(err, check.IsNil)
	backend1OrigAddr, err := s.Router.Addr(backend1)
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(backend1, addr1)
	c.Assert(err, check.IsNil)
	err = s.Router.AddBackend(backend2)
	c.Assert(err, check.IsNil)
	backend2OrigAddr, err := s.Router.Addr(backend2)
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(backend2, addr2)
	c.Assert(err, check.IsNil)
	isSwapped, swappedWith, err := router.IsSwapped(backend1)
	c.Assert(err, check.IsNil)
	c.Assert(isSwapped, check.Equals, false)
	isSwapped, swappedWith, err = router.IsSwapped(backend2)
	c.Assert(err, check.IsNil)
	c.Assert(isSwapped, check.Equals, false)
	c.Assert(swappedWith, check.Equals, backend2)
	err = s.Router.Swap(backend1, backend2, false)
	c.Assert(err, check.IsNil)
	isSwapped, swappedWith, err = router.IsSwapped(backend1)
	c.Assert(err, check.IsNil)
	c.Assert(isSwapped, check.Equals, true)
	c.Assert(swappedWith, check.Equals, backend2)
	isSwapped, swappedWith, err = router.IsSwapped(backend2)
	c.Assert(err, check.IsNil)
	c.Assert(isSwapped, check.Equals, true)
	c.Assert(swappedWith, check.Equals, backend1)
	backAddr1, err := s.Router.Addr(backend1)
	c.Assert(err, check.IsNil)
	c.Assert(backAddr1, check.Equals, backend2OrigAddr)
	backAddr2, err := s.Router.Addr(backend2)
	c.Assert(err, check.IsNil)
	c.Assert(backAddr2, check.Equals, backend1OrigAddr)
	routes, err := s.Router.Routes(backend1)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{addr1})
	routes, err = s.Router.Routes(backend2)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{addr2})
	err = s.Router.Swap(backend1, backend2, false)
	c.Assert(err, check.IsNil)
	isSwapped, swappedWith, err = router.IsSwapped(backend1)
	c.Assert(err, check.IsNil)
	c.Assert(isSwapped, check.Equals, false)
	c.Assert(swappedWith, check.Equals, backend1)
	backAddr1, err = s.Router.Addr(backend1)
	c.Assert(err, check.IsNil)
	c.Assert(backAddr1, check.Equals, backend1OrigAddr)
	backAddr2, err = s.Router.Addr(backend2)
	c.Assert(err, check.IsNil)
	c.Assert(backAddr2, check.Equals, backend2OrigAddr)
	routes, err = s.Router.Routes(backend1)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{addr1})
	routes, err = s.Router.Routes(backend2)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{addr2})
	err = s.Router.RemoveBackend(backend1)
	c.Assert(err, check.IsNil)
	err = s.Router.RemoveBackend(backend2)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestRouteAddDupCName(c *check.C) {
	cnameRouter, ok := s.Router.(router.CNameRouter)
	if !ok {
		c.Skip(fmt.Sprintf("%T does not implement CNameRouter", s.Router))
	}
	name := "backend1"
	err := cnameRouter.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	err = cnameRouter.AddRoute(name, addr1)
	c.Assert(err, check.IsNil)
	err = cnameRouter.SetCName("my.host.com", name)
	c.Assert(err, check.IsNil)
	err = cnameRouter.SetCName("my.host.com", name)
	c.Assert(err, check.Equals, router.ErrCNameExists)
	err = cnameRouter.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestCNames(c *check.C) {
	cnameRouter, ok := s.Router.(router.CNameRouter)
	if !ok {
		c.Skip(fmt.Sprintf("%T does not implement CNameRouter", s.Router))
	}
	name := "backend1"
	err := cnameRouter.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	err = cnameRouter.AddRoute(name, addr1)
	c.Assert(err, check.IsNil)
	err = cnameRouter.SetCName("my.host.com", name)
	c.Assert(err, check.IsNil)
	err = cnameRouter.SetCName("my.host2.com", name)
	c.Assert(err, check.IsNil)
	cnames, err := cnameRouter.CNames(name)
	url1, err := url.Parse("my.host.com")
	c.Assert(err, check.IsNil)
	url2, err := url.Parse("my.host2.com")
	c.Assert(err, check.IsNil)
	c.Assert(err, check.IsNil)
	expected := []*url.URL{url1, url2}
	sort.Sort(URLList(cnames))
	c.Assert(cnames, check.DeepEquals, expected)
	err = cnameRouter.UnsetCName("my.host.com", name)
	c.Assert(err, check.IsNil)
	err = cnameRouter.UnsetCName("my.host.com", name)
	c.Assert(err, check.Equals, router.ErrCNameNotFound)
	err = cnameRouter.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestSetUnsetCName(c *check.C) {
	cnameRouter, ok := s.Router.(router.CNameRouter)
	if !ok {
		c.Skip(fmt.Sprintf("%T does not implement CNameRouter", s.Router))
	}
	name := "backend1"
	err := cnameRouter.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	err = cnameRouter.AddRoute(name, addr1)
	c.Assert(err, check.IsNil)
	err = cnameRouter.SetCName("my.host.com", name)
	c.Assert(err, check.IsNil)
	err = cnameRouter.UnsetCName("my.host.com", name)
	c.Assert(err, check.IsNil)
	err = cnameRouter.UnsetCName("my.host.com", name)
	c.Assert(err, check.Equals, router.ErrCNameNotFound)
	err = cnameRouter.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestSetCNameInvalidBackend(c *check.C) {
	cnameRouter, ok := s.Router.(router.CNameRouter)
	if !ok {
		c.Skip(fmt.Sprintf("%T does not implement CNameRouter", s.Router))
	}
	err := cnameRouter.SetCName("my.cname", "backend1")
	c.Assert(err, check.Equals, router.ErrBackendNotFound)
}

func (s *RouterSuite) TestSetCNameSubdomainError(c *check.C) {
	cnameRouter, ok := s.Router.(router.CNameRouter)
	if !ok {
		c.Skip(fmt.Sprintf("%T does not implement CNameRouter", s.Router))
	}
	name := "backend1"
	err := cnameRouter.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	err = cnameRouter.AddRoute(name, addr1)
	c.Assert(err, check.IsNil)
	err = cnameRouter.SetCName("my.host.com", name)
	c.Assert(err, check.IsNil)
	addr, err := cnameRouter.Addr(name)
	c.Assert(err, check.IsNil)
	err = cnameRouter.SetCName("sub."+addr, name)
	c.Assert(err, check.Equals, router.ErrCNameNotAllowed)
	err = cnameRouter.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestRemoveBackendWithCName(c *check.C) {
	cnameRouter, ok := s.Router.(router.CNameRouter)
	if !ok {
		c.Skip(fmt.Sprintf("%T does not implement CNameRouter", s.Router))
	}
	name := "backend1"
	err := cnameRouter.AddBackend(name)
	c.Assert(err, check.IsNil)
	addr1, err := url.Parse("http://10.10.10.10:8080")
	c.Assert(err, check.IsNil)
	err = cnameRouter.AddRoute(name, addr1)
	c.Assert(err, check.IsNil)
	err = cnameRouter.SetCName("my.host.com", name)
	c.Assert(err, check.IsNil)
	err = cnameRouter.RemoveBackend(name)
	c.Assert(err, check.IsNil)
	err = cnameRouter.AddBackend(name)
	c.Assert(err, check.IsNil)
	err = cnameRouter.SetCName("my.host.com", name)
	c.Assert(err, check.IsNil)
	err = cnameRouter.RemoveBackend(name)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestRemoveBackendAfterSwap(c *check.C) {
	backend1 := "mybackend1"
	backend2 := "mybackend2"
	addr1, _ := url.Parse("http://127.0.0.1")
	addr2, _ := url.Parse("http://10.10.10.10")
	err := s.Router.AddBackend(backend1)
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(backend1, addr1)
	c.Assert(err, check.IsNil)
	err = s.Router.AddBackend(backend2)
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(backend2, addr2)
	c.Assert(err, check.IsNil)
	err = s.Router.Swap(backend1, backend2, false)
	c.Assert(err, check.IsNil)
	err = s.Router.RemoveBackend(backend1)
	c.Assert(err, check.Equals, router.ErrBackendSwapped)
	err = s.Router.Swap(backend1, backend2, false)
	c.Assert(err, check.IsNil)
	err = s.Router.RemoveBackend(backend1)
	c.Assert(err, check.IsNil)
	err = s.Router.RemoveBackend(backend2)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestRemoveBackendWithoutRemoveRoutes(c *check.C) {
	backend1 := "mybackend1"
	addr1, _ := url.Parse("http://127.0.0.1")
	addr2, _ := url.Parse("http://10.10.10.10")
	err := s.Router.AddBackend(backend1)
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(backend1, addr1)
	c.Assert(err, check.IsNil)
	err = s.Router.AddRoute(backend1, addr2)
	c.Assert(err, check.IsNil)
	err = s.Router.RemoveBackend(backend1)
	c.Assert(err, check.IsNil)
	err = s.Router.AddBackend(backend1)
	c.Assert(err, check.IsNil)
	routes, err := s.Router.Routes(backend1)
	c.Assert(err, check.IsNil)
	c.Assert(routes, HostEquals, []*url.URL{})
	err = s.Router.RemoveBackend(backend1)
	c.Assert(err, check.IsNil)
}

func (s *RouterSuite) TestSetHealthcheck(c *check.C) {
	hcRouter, ok := s.Router.(router.CustomHealthcheckRouter)
	if !ok {
		c.Skip(fmt.Sprintf("%T does not implement CustomHealthcheckRouter", s.Router))
	}
	err := s.Router.AddBackend("mybackend")
	c.Assert(err, check.IsNil)
	hcData := router.HealthcheckData{
		Path:   "/",
		Status: 200,
		Body:   "WORKING",
	}
	err = hcRouter.SetHealthcheck("mybackend", hcData)
	c.Assert(err, check.IsNil)
	err = s.Router.RemoveBackend("mybackend")
	c.Assert(err, check.IsNil)
}
