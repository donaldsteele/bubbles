//go:build darwin

package service

func platformManager(o Options) Manager { return newLaunchd(o) }
