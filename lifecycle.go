// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package FasterEdge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const defaultShutdownTimeout = 5 * time.Second

type runOptions struct{ shutdownTimeout time.Duration }

type RunOption func(*runOptions) error

func WithShutdownTimeout(timeout time.Duration) RunOption {
	return func(options *runOptions) error {
		if timeout <= 0 {
			return fmt.Errorf("shutdown timeout %s: %w", timeout, errors.Join(types.ErrInvalidArguments, types.ErrInvalidShutdownTimeout))
		}
		options.shutdownTimeout = timeout
		return nil
	}
}

func CloseAtom(ctx context.Context, atom *types.Atom, opts ...RunOption) error {
	if ctx == nil {
		return types.ErrNilContext
	}
	if atom == nil {
		return types.ErrNilAtom
	}
	options := runOptions{shutdownTimeout: defaultShutdownTimeout}
	for _, option := range opts {
		if option == nil {
			return fmt.Errorf("nil RunOption: %w", types.ErrInvalidArguments)
		}
		if err := option(&options); err != nil {
			return err
		}
	}
	return atom.Close(ctx, options.shutdownTimeout)
}

func UnmountAtom(ctx context.Context, atom *types.Atom, opts ...RunOption) error {
	return CloseAtom(ctx, atom, opts...)
}

func RunAtom(ctx context.Context, atom *types.Atom, opts ...RunOption) error {
	if ctx == nil {
		return types.ErrNilContext
	}
	if atom == nil {
		return types.ErrNilAtom
	}
	options := runOptions{shutdownTimeout: defaultShutdownTimeout}
	for _, option := range opts {
		if option == nil {
			return fmt.Errorf("nil RunOption: %w", types.ErrInvalidArguments)
		}
		if err := option(&options); err != nil {
			return err
		}
	}
	return atom.RunAll(ctx, options.shutdownTimeout)
}
