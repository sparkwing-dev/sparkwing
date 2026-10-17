package wingd

// NewHostSampler observes this process's machine and container resources.
func NewHostSampler() HostSampler {
	return &machineSampler{host: &platformSampler{}, container: newContainerSensor("")}
}

type machineSampler struct {
	host      HostSampler
	container *containerSensor
}

func (s *machineSampler) Sample() (HostStat, error) {
	stat, err := s.host.Sample()
	if err != nil {
		return stat, err
	}
	return s.container.apply(stat), nil
}
