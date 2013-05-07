// Copyright 2013 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package testing

import (
	"bytes"
	"github.com/globocom/tsuru/exec"
	"launchpad.net/gocheck"
	"testing"
)

type S struct{}

var _ = gocheck.Suite(&S{})

func Test(t *testing.T) { gocheck.TestingT(t) }

func (s *S) TestCommandGetName(c *gocheck.C) {
	cmd := command{name: "docker", args: []string{"run", "some/img"}}
	c.Assert(cmd.GetName(), gocheck.Equals, cmd.name)
}

func (s *S) TestCommandGetArgs(c *gocheck.C) {
	cmd := command{name: "docker", args: []string{"run", "some/img"}}
	c.Assert(cmd.GetArgs(), gocheck.DeepEquals, cmd.args)
}

func (s *S) TestFakeExecutorImplementsExecutor(c *gocheck.C) {
	var _ exec.Executor = &FakeExecutor{}
}

func (s *S) TestExecute(c *gocheck.C) {
	var e FakeExecutor
	var b bytes.Buffer
	cmd := "ls"
	args := []string{"-lsa"}
	err := e.Execute(cmd, args, nil, &b, &b)
	c.Assert(err, gocheck.IsNil)
	cmd = "ps"
	args = []string{"aux"}
	err = e.Execute(cmd, args, nil, &b, &b)
	c.Assert(err, gocheck.IsNil)
	cmd = "ps"
	args = []string{"-ef"}
	err = e.Execute(cmd, args, nil, &b, &b)
	c.Assert(err, gocheck.IsNil)
	c.Assert(e.ExecutedCmd("ls", []string{"-lsa"}), gocheck.Equals, true)
	c.Assert(e.ExecutedCmd("ps", []string{"aux"}), gocheck.Equals, true)
	c.Assert(e.ExecutedCmd("ps", []string{"-ef"}), gocheck.Equals, true)
}

func (s *S) TestFakeExecutorOutput(c *gocheck.C) {
	e := FakeExecutor{Output: map[string][]byte{"*": []byte("ble")}}
	var b bytes.Buffer
	cmd := "ls"
	args := []string{"-lsa"}
	err := e.Execute(cmd, args, nil, &b, &b)
	c.Assert(err, gocheck.IsNil)
	c.Assert(e.ExecutedCmd("ls", []string{"-lsa"}), gocheck.Equals, true)
	c.Assert(b.String(), gocheck.Equals, "ble")
}

func (s *S) TestFakeExecutorHasOutputForAnyArgsUsingWildCard(c *gocheck.C) {
	e := FakeExecutor{Output: map[string][]byte{"*": []byte("ble")}}
	args := []string{"-i", "-t"}
	has, _ := e.hasOutputForArgs(args)
	c.Assert(has, gocheck.Equals, true)
}

func (s *S) TestFakeExecutorHasOutputForArgsSpecifyingArgs(c *gocheck.C) {
	e := FakeExecutor{Output: map[string][]byte{"-i -t": []byte("ble")}}
	args := []string{"-i", "-t"}
	has, _ := e.hasOutputForArgs(args)
	c.Assert(has, gocheck.Equals, true)
}

func (s *S) TestFakeExecutorDoesNotHasOutputForNoMatchingArgs(c *gocheck.C) {
	e := FakeExecutor{Output: map[string][]byte{"-t -i": []byte("ble")}}
	args := []string{"-d", "-f"}
	has, _ := e.hasOutputForArgs(args)
	c.Assert(has, gocheck.Equals, false)
}

func (s *S) TestErrorExecutorImplementsExecutor(c *gocheck.C) {
	var _ exec.Executor = &ErrorExecutor{}
}

func (s *S) TestErrorExecute(c *gocheck.C) {
	var e ErrorExecutor
	var b bytes.Buffer
	cmd := "ls"
	args := []string{"-lsa"}
	err := e.Execute(cmd, args, nil, &b, &b)
	c.Assert(err, gocheck.NotNil)
	cmd = "ps"
	args = []string{"aux"}
	err = e.Execute(cmd, args, nil, &b, &b)
	c.Assert(err, gocheck.NotNil)
	cmd = "ps"
	args = []string{"-ef"}
	err = e.Execute(cmd, args, nil, &b, &b)
	c.Assert(err, gocheck.NotNil)
	c.Assert(e.ExecutedCmd("ls", []string{"-lsa"}), gocheck.Equals, true)
	c.Assert(e.ExecutedCmd("ps", []string{"aux"}), gocheck.Equals, true)
	c.Assert(e.ExecutedCmd("ps", []string{"-ef"}), gocheck.Equals, true)
}

func (s *S) TestErrorExecutorOutput(c *gocheck.C) {
	e := ErrorExecutor{Output: map[string][]byte{"*": []byte("ble")}}
	var b bytes.Buffer
	cmd := "ls"
	args := []string{"-lsa"}
	err := e.Execute(cmd, args, nil, &b, &b)
	c.Assert(err, gocheck.NotNil)
	c.Assert(e.ExecutedCmd("ls", []string{"-lsa"}), gocheck.Equals, true)
	c.Assert(b.String(), gocheck.Equals, "ble")
}

func (s *S) TestErrorExecutorHasOutputForAnyArgsUsingWildCard(c *gocheck.C) {
	e := ErrorExecutor{Output: map[string][]byte{"*": []byte("ble")}}
	args := []string{"-i", "-t"}
	has, _ := e.hasOutputForArgs(args)
	c.Assert(has, gocheck.Equals, true)
}

func (s *S) TestErrorExecutorHasOutputForArgsSpecifyingArgs(c *gocheck.C) {
	e := ErrorExecutor{Output: map[string][]byte{"-i -t": []byte("ble")}}
	args := []string{"-i", "-t"}
	has, _ := e.hasOutputForArgs(args)
	c.Assert(has, gocheck.Equals, true)
}

func (s *S) TestErrorExecutorDoesNotHasOutputForNoMatchingArgs(c *gocheck.C) {
	e := ErrorExecutor{Output: map[string][]byte{"-t -i": []byte("ble")}}
	args := []string{"-d", "-f"}
	has, _ := e.hasOutputForArgs(args)
	c.Assert(has, gocheck.Equals, false)
}

func (s *S) TestGetCommands(c *gocheck.C) {
	e := FakeExecutor{}
	b := &bytes.Buffer{}
	err := e.Execute("sudo", []string{"ifconfig", "-a"}, nil, b, b)
	c.Assert(err, gocheck.IsNil)
	cmds := e.GetCommands("sudo")
	expected := []command{{name: "sudo", args: []string{"ifconfig", "-a"}}}
	c.Assert(cmds, gocheck.DeepEquals, expected)
}
