package masterclient

import (
	"context"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (cl *Client) ReadMemory(ctx context.Context, in c.MemoryQuery) (out c.Memory, err error) {
	err = cl.call(ctx, "POST", "/api/v1/memory/read", in, &out)
	return
}

func (cl *Client) WriteMemory(ctx context.Context, in c.MemoryWrite) (out c.Memory, err error) {
	err = cl.call(ctx, "POST", "/api/v1/memory/write", in, &out)
	return
}

func (cl *Client) ListMemory(ctx context.Context, in c.MemoryList) (out c.MemoryPage, err error) {
	err = cl.call(ctx, "POST", "/api/v1/memory/list", in, &out)
	return
}
