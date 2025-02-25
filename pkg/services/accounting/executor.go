package accounting

import (
	"context"
	"fmt"

	"github.com/TrueCloudLab/frostfs-api-go/v2/accounting"
)

type ServiceExecutor interface {
	Balance(context.Context, *accounting.BalanceRequestBody) (*accounting.BalanceResponseBody, error)
}

type executorSvc struct {
	exec ServiceExecutor
}

// NewExecutionService wraps ServiceExecutor and returns Accounting Service interface.
func NewExecutionService(exec ServiceExecutor) Server {
	return &executorSvc{
		exec: exec,
	}
}

func (s *executorSvc) Balance(ctx context.Context, req *accounting.BalanceRequest) (*accounting.BalanceResponse, error) {
	respBody, err := s.exec.Balance(ctx, req.GetBody())
	if err != nil {
		return nil, fmt.Errorf("could not execute Balance request: %w", err)
	}

	resp := new(accounting.BalanceResponse)
	resp.SetBody(respBody)

	return resp, nil
}
