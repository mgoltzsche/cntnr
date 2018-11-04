package net

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/cni/pkg/version"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	//"github.com/vishvananda/netns"
)

type NetConfigs struct {
	confDir string
}

func NewNetConfigs(confDir string) (*NetConfigs, error) {
	if confDir == "" {
		confDir = os.Getenv("NETCONFPATH")
		if confDir == "" {
			confDir = "/etc/cni/net.d"
		}
	}
	confFiles, err := libcni.ConfFiles(confDir, []string{".conf", ".json"})
	if err != nil {
		return nil, errors.Wrap(err, "read CNI network configuration")
	}
	sort.Strings(confFiles)
	return &NetConfigs{confDir}, nil
}

func (n *NetConfigs) GetConfig(name string) (*libcni.NetworkConfigList, error) {
	l, err := libcni.LoadConfList(n.confDir, name)
	if err != nil && name == "default" {
		_, noConfDir := err.(libcni.NoConfigsFoundError)
		_, confNotFound := err.(libcni.NotFoundError)
		if noConfDir || confNotFound {
			return defaultNetConf()
		}
	}
	return l, err
}

func defaultNetConf() (cfg *libcni.NetworkConfigList, err error) {
	ipamDataDir := os.Getenv("IPAMDATADIR")
	if ipamDataDir == "" {
		return nil, errors.New("default net conf: IPAMDATADIR env var not set")
	}
	rawConfigList := map[string]interface{}{
		"cniVersion": version.Current(),
		"name":       "default",
		"plugins": []interface{}{
			map[string]interface{}{
				"cniVersion": version.Current(),
				"type":       "ptp",
				"ipMasq":     true,
				"ipam": map[string]interface{}{
					"type":   "host-local",
					"subnet": "10.1.0.0/24",
					"routes": []interface{}{
						map[string]interface{}{
							"dst": "0.0.0.0/0",
						},
					},
					"dataDir": ipamDataDir,
				},
				"dns": map[string]interface{}{
					"nameservers": []string{"1.1.1.1"},
				},
			},
		},
	}
	b, err := json.Marshal(rawConfigList)
	if err == nil {
		cfg, err = libcni.ConfListFromBytes(b)
	}
	return cfg, errors.Wrap(err, "load default config")
}

func MapPorts(original *libcni.NetworkConfigList, portMap []PortMapEntry) (cfg *libcni.NetworkConfigList, err error) {
	if len(portMap) == 0 {
		return original, nil
	}
	rawPlugins := make([]interface{}, len(original.Plugins)+1)
	for i, plugin := range original.Plugins {
		rawPlugin := make(map[string]interface{})
		if err := json.Unmarshal(plugin.Bytes, &rawPlugin); err != nil {
			return nil, err
		}
		rawPlugins[i] = rawPlugin
	}
	rawPlugins[len(original.Plugins)] = map[string]interface{}{
		"cniVersion": version.Current(),
		"type":       "portmap",
		"runtimeConfig": map[string]interface{}{
			"portMappings": portMap,
		},
		"snat": true, // snat=true allows localhost port mapping access but adds another rule
	}
	rawConfigList := map[string]interface{}{
		"name":       original.Name,
		"cniVersion": original.CNIVersion,
		"plugins":    rawPlugins,
	}
	b, err := json.Marshal(rawConfigList)
	if err == nil {
		cfg, err = libcni.ConfListFromBytes(b)
	}
	return cfg, errors.Wrap(err, "load portmap config")
}

type NetManager struct {
	id             string
	netNS          string
	cniArgs        [][2]string
	capabilityArgs map[string]interface{}
	cni            *libcni.CNIConfig
}

func NewNetManager(state *specs.State) (r *NetManager, err error) {
	netPaths := filepath.SplitList(os.Getenv("CNI_PATH"))
	if len(netPaths) == 0 {
		netPaths = []string{"/var/lib/cni"}
	}

	// Parse CNI_ARGS
	var cniArgs [][2]string
	args := os.Getenv("CNI_ARGS")
	if len(args) > 0 {
		cniArgs, err = parseCniArgs(args)
		if err != nil {
			return nil, err
		}
	}

	// Parse CAP_ARGS
	var capabilityArgs map[string]interface{}
	capabilityArgsValue := os.Getenv("CAP_ARGS")
	if len(capabilityArgsValue) > 0 {
		if err = json.Unmarshal([]byte(capabilityArgsValue), &capabilityArgs); err != nil {
			return nil, errors.Wrap(err, "read CAP_ARGS")
		}
	}

	var netns string
	if state.Pid > 0 {
		netns = fmt.Sprintf("/proc/%d/ns/net", state.Pid)
	}

	r = &NetManager{
		id:             state.ID,
		netNS:          netns,
		cniArgs:        cniArgs,
		capabilityArgs: capabilityArgs,
		cni:            &libcni.CNIConfig{Path: netPaths},
	}

	return
}

// Resolves the configured CNI network by name
// and adds it to the container process' network namespace.
func (m *NetManager) AddNet(ifName string, netConf *libcni.NetworkConfigList) (r *current.Result, err error) {
	rs, err := m.cni.AddNetworkList(netConf, m.rtConf(ifName))
	if err != nil {
		return nil, errors.Wrap(err, "add CNI network "+netConf.Name)
	}
	r, err = current.NewResultFromResult(rs)
	return r, errors.Wrap(err, "convert CNI result for network "+netConf.Name)
}

func (m *NetManager) DelNet(ifName string, netConf *libcni.NetworkConfigList) (err error) {
	return m.cni.DelNetworkList(netConf, m.rtConf(ifName))
}

func (m *NetManager) rtConf(ifName string) *libcni.RuntimeConf {
	return &libcni.RuntimeConf{
		ContainerID:    m.id,
		NetNS:          m.netNS,
		IfName:         ifName,
		Args:           m.cniArgs,
		CapabilityArgs: m.capabilityArgs,
	}
}

func parseCniArgs(args string) ([][2]string, error) {
	var result [][2]string

	pairs := strings.Split(args, ";")
	for _, pair := range pairs {
		kv := strings.Split(pair, "=")
		if len(kv) != 2 || kv[0] == "" || kv[1] == "" {
			return nil, errors.Errorf("invalid CNI_ARGS pair %q", pair)
		}

		result = append(result, [2]string{kv[0], kv[1]})
	}

	return result, nil
}

func CreateNetNS(file string) error {
	// TODO: clean this up
	if strings.Index(file, "/var/run/netns/") != 0 {
		return errors.New("Only named network namespaces in /var/run/netns/ are supported")
	}
	name := file[15:]
	return runCmd("ip", "netns", "add", name)
	/* // anonymous network namespace
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ns, err := netns.New()
	if err != nil {
		return err
	}
	defer ns.Close()
	return fmt.Sprint("/proc/self/fd/", int(ns)), nil */
}

func DelNetNS(file string) error {
	// TODO: clean this up
	if strings.Index(file, "/var/run/netns/") != 0 {
		return errors.New("Only named network namespaces in /var/run/netns/ are supported")
	}
	name := file[15:]
	return runCmd("ip", "netns", "delete", name)
}

func runCmd(c string, args ...string) error {
	cmd := exec.Command(c, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return errors.Wrapf(err, "%s: %s", strings.Join(append([]string{c}, args...), " "), out.String())
	}
	return nil
}
