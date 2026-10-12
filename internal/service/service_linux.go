//go:build linux

package service

func platformManager(o Options) Manager { return newSystemd(o) }
