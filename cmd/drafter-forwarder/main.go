package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/coreos/go-iptables/iptables"
	"github.com/vishvananda/netns"
)

var (
	errNoInternalHostVethFound = errors.New("no internal host veth found")
)

const (
	loopbackAddr = "127.0.0.1"
)

type forwardConfiguration struct {
	Netns        string `json:"netns"`
	InternalPort int    `json:"internalPort"`
	ExternalAddr string `json:"externalAddr"`
	Protocol     string `json:"protocol"`
}

func main() {
	rawHostVethCIDR := flag.String("host-veth-cidr", "10.0.8.0/22", "CIDR for the veths outside the namespace")

	defaultForwards, err := json.Marshal([]forwardConfiguration{
		{
			Netns:        "ark0",
			InternalPort: 6379,
			ExternalAddr: "127.0.0.1:3333",
			Protocol:     "tcp",
		},
	})
	if err != nil {
		panic(err)
	}

	rawForwards := flag.String("forwards", string(defaultForwards), "Forwards configuration (wildcard IPs like 0.0.0.0 are not valid, be explict)")

	flag.Parse()

	var forwards []forwardConfiguration
	if err := json.Unmarshal([]byte(*rawForwards), &forwards); err != nil {
		panic(err)
	}

	_, hostVethCIDR, err := net.ParseCIDR(*rawHostVethCIDR)
	if err != nil {
		panic(err)
	}

	iptable, err := iptables.New(
		iptables.IPFamily(iptables.ProtocolIPv4),
		iptables.Timeout(60),
	)
	if err != nil {
		panic(err)
	}

	for _, forward := range forwards {
		host, port, err := net.SplitHostPort(forward.ExternalAddr)
		if err != nil {
			panic(err)
		}

		if host == loopbackAddr {
			if err := os.WriteFile(filepath.Join("/proc", "sys", "net", "ipv4", "conf", "all", "route_localnet"), []byte("1"), os.ModePerm); err != nil {
				panic(err)
			}

			if err := os.WriteFile(filepath.Join("/proc", "sys", "net", "ipv4", "conf", "lo", "route_localnet"), []byte("1"), os.ModePerm); err != nil {
				panic(err)
			}
		}

		hostVethInternalIP, err := func() (string, error) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()

			originalNSHandle, err := netns.Get()
			if err != nil {
				return "", err
			}
			defer originalNSHandle.Close()
			defer netns.Set(originalNSHandle)

			nsHandle, err := netns.GetFromName(forward.Netns)
			if err != nil {
				return "", err
			}
			defer nsHandle.Close()

			if err := netns.Set(nsHandle); err != nil {
				return "", err
			}

			interfaces, err := net.Interfaces()
			if err != nil {
				return "", err
			}

			for _, iface := range interfaces {
				addrs, err := iface.Addrs()
				if err != nil {
					return "", err
				}

				for _, addr := range addrs {
					var ip net.IP
					switch v := addr.(type) {
					case *net.IPNet:
						ip = v.IP
					case *net.IPAddr:
						ip = v.IP

					default:
						continue
					}

					if ip.IsLoopback() || ip.To4() == nil {
						continue
					}

					if hostVethCIDR.Contains(ip) {
						return ip.String(), nil
					}
				}
			}

			return "", errNoInternalHostVethFound
		}()
		if err != nil {
			panic(err)
		}

		if host != loopbackAddr {
			if err := iptable.Append("filter", "FORWARD", "-d", hostVethInternalIP, "-j", "ACCEPT"); err != nil {
				panic(err)
			}
			defer iptable.Delete("filter", "FORWARD", "-d", hostVethInternalIP, "-j", "ACCEPT")

			if err := iptable.Append("filter", "FORWARD", "-s", hostVethInternalIP, "-j", "ACCEPT"); err != nil {
				panic(err)
			}
			defer iptable.Delete("filter", "FORWARD", "-s", hostVethInternalIP, "-j", "ACCEPT")
		}

		log.Println(forward.Netns, hostVethInternalIP, forward.InternalPort, "->", host, port, forward.Protocol)

		if err := iptable.Append("nat", "OUTPUT", "-p", forward.Protocol, "-d", host, "--dport", port, "-j", "DNAT", "--to-destination", net.JoinHostPort(hostVethInternalIP, fmt.Sprintf("%v", forward.InternalPort))); err != nil {
			panic(err)
		}
		defer iptable.Delete("nat", "OUTPUT", "-p", forward.Protocol, "-d", host, "--dport", port, "-j", "DNAT", "--to-destination", net.JoinHostPort(hostVethInternalIP, fmt.Sprintf("%v", forward.InternalPort)))

		if err := iptable.Append("nat", "PREROUTING", "-p", forward.Protocol, "--dport", port, "-d", host, "-j", "DNAT", "--to-destination", net.JoinHostPort(hostVethInternalIP, fmt.Sprintf("%v", forward.InternalPort))); err != nil {
			panic(err)
		}
		defer iptable.Delete("nat", "PREROUTING", "-p", forward.Protocol, "--dport", port, "-d", host, "-j", "DNAT", "--to-destination", net.JoinHostPort(hostVethInternalIP, fmt.Sprintf("%v", forward.InternalPort)))

		if err := iptable.Append("nat", "POSTROUTING", "-p", forward.Protocol, "-d", hostVethInternalIP, "--dport", fmt.Sprintf("%v", forward.InternalPort), "-j", "MASQUERADE"); err != nil {
			panic(err)
		}
		defer iptable.Delete("nat", "POSTROUTING", "-p", forward.Protocol, "-d", hostVethInternalIP, "--dport", fmt.Sprintf("%v", forward.InternalPort), "-j", "MASQUERADE")

		time.Sleep(time.Second * 10)
	}
}
