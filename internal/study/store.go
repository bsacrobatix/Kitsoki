package study

import "context"

type Store interface {
	Submit(context.Context, SubmitRequest) (Study, bool, error)
	Get(context.Context, string) (Snapshot, error)
	List(context.Context) ([]Study, error)
	Events(context.Context, string, int64) ([]Event, error)
	Retry(context.Context, string, string) (Attempt, error)
	Cancel(context.Context, string, string) error
	Record(context.Context, string, string, string, Result) error
}
