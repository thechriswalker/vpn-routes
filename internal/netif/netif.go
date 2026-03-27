package netif

import "net"

func Exists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

