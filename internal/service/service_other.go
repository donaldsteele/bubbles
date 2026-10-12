//go:build !linux && !darwin && !windows

package service

import "context"

type unsupportedManager struct{}

func platformManager(Options) Manager { return unsupportedManager{} }

func (unsupportedManager) Install(context.Context, Spec) error { return ErrUnsupported }
func (unsupportedManager) Uninstall(context.Context) error     { return ErrUnsupported }
func (unsupportedManager) Start(context.Context) error         { return ErrUnsupported }
func (unsupportedManager) Stop(context.Context) error          { return ErrUnsupported }
func (unsupportedManager) Status(context.Context) (string, error) {
	return "", ErrUnsupported
}
