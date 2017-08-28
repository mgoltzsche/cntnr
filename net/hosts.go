package net

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

func writeHostsFile(dest string, hostIpMap map[string]string, order []string) error {
	hosts := defaultHosts()
	for _, name := range order {
		ip := hostIpMap[name]
		hosts[ip] = strings.Trim(hosts[ip]+" "+name, " ")
	}
	entries := make([]string, len(hosts))
	i := 0
	for ip, names := range hosts {
		entries[i] = fmt.Sprintf("%-15s  %s", ip, names)
		i++
	}
	sort.Strings(entries)

	hc := "# Generated by " + os.Args[0] + "\n" + strings.Join(entries, "\n") + "\n"
	err := writeFile(dest, hc)
	if err != nil {
		return fmt.Errorf("Cannot write hosts file: %s", err)
	}
	return nil
}

func defaultHosts() map[string]string {
	return map[string]string{
		"127.0.0.1": "localhost localhost.localdomain localhost.domain localhost4 localhost4.localdomain4",
		"::1":       "ip6-localhost ip6-loopback localhost6 localhost6.localdomain6",
		"fe00::0":   "ip6-localnet",
		"ff00::0":   "ip6-mcastprefix",
		"ff02::1":   "ip6-allnodes",
		"ff02::2":   "ip6-allrouters",
		"ff02::3":   "ip6-allhosts",
	}
}
