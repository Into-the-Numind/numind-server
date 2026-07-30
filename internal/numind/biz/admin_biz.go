package biz

import (
	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/biz/monitor"
	sopbiz "numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/pricing"

	"github.com/spf13/viper"
)

// NewAdminBiz builds only the services used by the admin API. It intentionally
// does not initialize the user-side Agent runtime, Sandbox pool, Feishu
// workspace, memory workers, XHS workers, document runtime, or global biz.B.
func NewAdminBiz(ds store.IStore) *biz {
	membershipSvc := membership.NewMembershipService(ds.DB())
	creditBiz := credit.NewCreditBiz(ds)
	credit.InjectCreditBizMembershipSvc(creditBiz, membershipSvc)
	pricingCalc := pricing.NewCalculator(ds.Billing())
	creditSvc := credit.NewCreditService(ds, creditBiz, pricingCalc, membershipSvc)

	b := &biz{
		ds:            ds,
		credit:        creditBiz,
		creditService: creditSvc,
		pricing:       pricingCalc,
	}
	b.sopService = sopbiz.NewSopBiz(
		ds,
		sopbiz.NewSopExecutor(ds),
		creditBiz,
	).WithCreditService(creditSvc, pricingCalc)
	b.monitorService = monitor.NewMonitorBiz(
		ds,
		monitor.NewCooldownManager(
			viper.GetInt("monitor.cooldown.check_minutes"),
			viper.GetInt("monitor.cooldown.analyze_minutes"),
		),
	)
	return b
}
