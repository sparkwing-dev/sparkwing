package egress

import "github.com/prometheus/client_golang/prometheus"

var (
	dayBytesDesc = prometheus.NewDesc("sparkwing_egress_day_bytes",
		"Bytes this process has sent to clients in the current UTC day.", nil, nil)
	alarmDesc = prometheus.NewDesc("sparkwing_egress_daily_alarm",
		"1 while this process is past its daily egress alarm threshold, which refuses nothing.", nil, nil)
)

type collector struct{ meter func() *Meter }

// NewCollector reports the meter that meter returns as the
// sparkwing_egress_day_bytes and sparkwing_egress_daily_alarm gauges, read at
// scrape time. It reports nothing while meter returns nil.
func NewCollector(meter func() *Meter) prometheus.Collector { return collector{meter: meter} }

func (collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- dayBytesDesc
	ch <- alarmDesc
}

func (c collector) Collect(ch chan<- prometheus.Metric) {
	m := c.meter()
	if m == nil {
		return
	}
	state := m.State()
	alarm := 0.0
	if state.Alarm {
		alarm = 1
	}
	ch <- prometheus.MustNewConstMetric(dayBytesDesc, prometheus.GaugeValue, float64(state.GlobalDayBytes))
	ch <- prometheus.MustNewConstMetric(alarmDesc, prometheus.GaugeValue, alarm)
}
