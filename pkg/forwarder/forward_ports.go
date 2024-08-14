package forwarder

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"

	"github.com/coreos/go-iptables/iptables"
	"github.com/loopholelabs/drafter/internal/utils"
	"github.com/loopholelabs/goroutine-manager/pkg/manager"
	"github.com/vishvananda/netns"
)

type PortForward struct {
	Netns        string `json:"netns"`
	InternalPort string `json:"internalPort"`
	Protocol     string `json:"protocol"`

	ExternalAddr string `json:"externalAddr"`
}

type PortForwardHooks struct {
	OnAfterPortForward func(
		portID int,

		netns string,

		internalIP string,
		internalPort string,

		externalIP string,
		externalPort string,

		protocol string,
	)
	OnBeforePortUnforward func(portID int)
}

type ForwardedPorts struct {
	Wait  func() error
	Close func() error
}

func ForwardPorts(
	ctx context.Context,

	hostVethCIDR *net.IPNet,

	ports []PortForward,

	hooks PortForwardHooks,
) (forwardedPorts *ForwardedPorts, errs error) {
	forwardedPorts = &ForwardedPorts{
		Wait: func() error {
			return nil
		},
		Close: func() error {
			return nil
		},
	}

	goroutineManager := manager.NewGoroutineManager(
		ctx,
		&errs,
		manager.GoroutineManagerHooks{},
	)
	defer goroutineManager.Wait()
	defer goroutineManager.StopAllGoroutines()
	defer goroutineManager.CreateBackgroundPanicCollector()()

	iptable, err := iptables.New(
		iptables.IPFamily(iptables.ProtocolIPv4),
		iptables.Timeout(60),
	)
	if err != nil {
		panic(errors.Join(ErrCouldNotCreatingIPTables, err))
	}

	_, deferFuncs, err := utils.ConcurrentMap(
		ports,
		func(index int, input PortForward, _ *struct{}, addDefer func(deferFunc func() error)) error {
			select {
			case <-goroutineManager.Context().Done():
				return goroutineManager.Context().Err()

			default:
				break
			}

			host, port, err := net.SplitHostPort(input.ExternalAddr)
			if err != nil {
				return errors.Join(ErrCouldNotSplitHostPort, err)
			}

			hostIP, err := net.ResolveIPAddr("ip", host)
			if err != nil {
				return errors.Join(ErrCouldNotResolveIPAddr, err)
			}

			if hostIP.IP.IsLoopback() {
				if err := os.WriteFile(filepath.Join("/proc", "sys", "net", "ipv4", "conf", "all", "route_localnet"), []byte("1"), os.ModePerm); err != nil {
					return errors.Join(ErrCouldNotWriteFileRouteLocalnetAll, err)
				}

				if err := os.WriteFile(filepath.Join("/proc", "sys", "net", "ipv4", "conf", "lo", "route_localnet"), []byte("1"), os.ModePerm); err != nil {
					return errors.Join(ErrCouldNotWriteFileRouteLocalnetLo, err)
				}
			}

			hostVethInternalIP, err := func() (string, error) {
				runtime.LockOSThread()
				defer runtime.UnlockOSThread()

				originalNSHandle, err := netns.Get()
				if err != nil {
					return "", errors.Join(ErrCouldNotGetOriginalNSHandle, err)
				}
				defer originalNSHandle.Close()
				defer netns.Set(originalNSHandle)

				nsHandle, err := netns.GetFromName(input.Netns)
				if err != nil {
					return "", errors.Join(ErrCouldNotGetNSHandle, err)
				}
				defer nsHandle.Close()

				if err := netns.Set(nsHandle); err != nil {
					return "", errors.Join(ErrCouldNotSetNSHandle, err)
				}

				interfaces, err := net.Interfaces()
				if err != nil {
					return "", errors.Join(ErrCouldNotListInterfaces, err)
				}

				for _, iface := range interfaces {
					addrs, err := iface.Addrs()
					if err != nil {
						return "", errors.Join(ErrCouldNotGetInterfaceAddresses, err)
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

						if ip.IsLoopback() {
							continue
						}

						if hostVethCIDR.Contains(ip) {
							return ip.String(), nil
						}
					}
				}

				return "", ErrCouldNotFindInternalHostVeth
			}()
			if err != nil {
				return err
			}

			select {
			case <-goroutineManager.Context().Done():
				return goroutineManager.Context().Err()

			default:
				break
			}

			if !hostIP.IP.IsLoopback() {
				if err := iptable.Append("filter", "FORWARD", "-d", hostVethInternalIP, "-j", "ACCEPT"); err != nil {
					return errors.Join(ErrCouldNotAppendIPTablesFilterForwardD, err)
				}
				addDefer(func() error {
					return iptable.Delete("filter", "FORWARD", "-d", hostVethInternalIP, "-j", "ACCEPT")
				})

				if err := iptable.Append("filter", "FORWARD", "-s", hostVethInternalIP, "-j", "ACCEPT"); err != nil {
					return errors.Join(ErrCouldNotAppendIPTablesFilterForwardS, err)
				}
				addDefer(func() error {
					return iptable.Delete("filter", "FORWARD", "-s", hostVethInternalIP, "-j", "ACCEPT")
				})
			}

			select {
			case <-goroutineManager.Context().Done():
				return goroutineManager.Context().Err()

			default:
				break
			}

			if err := iptable.Append("nat", "OUTPUT", "-p", input.Protocol, "-d", host, "--dport", port, "-j", "DNAT", "--to-destination", net.JoinHostPort(hostVethInternalIP, fmt.Sprintf("%v", input.InternalPort))); err != nil {
				return errors.Join(ErrCouldNotAppendIPTablesNatOutput, err)
			}
			addDefer(func() error {
				return iptable.Delete("nat", "OUTPUT", "-p", input.Protocol, "-d", host, "--dport", port, "-j", "DNAT", "--to-destination", net.JoinHostPort(hostVethInternalIP, fmt.Sprintf("%v", input.InternalPort)))
			})

			if err := iptable.Append("nat", "PREROUTING", "-p", input.Protocol, "--dport", port, "-d", host, "-j", "DNAT", "--to-destination", net.JoinHostPort(hostVethInternalIP, fmt.Sprintf("%v", input.InternalPort))); err != nil {
				return errors.Join(ErrCouldNotAppendIPTablesNatPrerouting, err)
			}
			addDefer(func() error {
				return iptable.Delete("nat", "PREROUTING", "-p", input.Protocol, "--dport", port, "-d", host, "-j", "DNAT", "--to-destination", net.JoinHostPort(hostVethInternalIP, fmt.Sprintf("%v", input.InternalPort)))
			})

			if err := iptable.Append("nat", "POSTROUTING", "-p", input.Protocol, "-d", hostVethInternalIP, "--dport", fmt.Sprintf("%v", input.InternalPort), "-j", "MASQUERADE"); err != nil {
				return errors.Join(ErrCouldNotAppendIPTablesNatPostrouting, err)
			}
			addDefer(func() error {
				return iptable.Delete("nat", "POSTROUTING", "-p", input.Protocol, "-d", hostVethInternalIP, "--dport", fmt.Sprintf("%v", input.InternalPort), "-j", "MASQUERADE")
			})

			if hook := hooks.OnAfterPortForward; hook != nil {
				hook(
					index,

					input.Netns,

					hostVethInternalIP,
					input.InternalPort,

					host,
					port,

					input.Protocol,
				)
			}

			addDefer(func() error {
				if hook := hooks.OnBeforePortUnforward; hook != nil {
					hook(index)
				}

				return nil
			})

			return nil
		},
	)

	closeInProgress := make(chan any)
	forwardedPorts.Close = func() (errs error) {
		defer close(closeInProgress)

		for _, closeFuncs := range deferFuncs {
			for _, closeFunc := range closeFuncs {
				defer func(closeFunc func() error) {
					if err := closeFunc(); err != nil {
						errs = errors.Join(errs, err)
					}
				}(closeFunc)
			}
		}

		return
	}
	// Future-proofing; if we decide that port-forwarding should use a background copy loop like `socat`, we can wait for that loop to finish here and return any errors
	forwardedPorts.Wait = func() error {
		<-closeInProgress

		return nil
	}

	// No need for the usual `handleGoroutinePanic` & context cancellation here since we only have a single error return, and in this return close on any errors
	// It's easier this way because otherwise we would have to `select` between `ctx` and default before each call that adds to `deferFuncs` like `addDefer`
	if err != nil {
		// Make sure that we schedule the `deferFuncs` even if we get an error during setup
		err = errors.Join(err, forwardedPorts.Close()) // We intentionally append any errors during close at the end so that we don't shadow the actual cause

		panic(err)
	}

	return
}
