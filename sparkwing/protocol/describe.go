package nodeproto

// Pipeline selects SDK metadata by its configured entrypoint.
func (d DescribeDoc) Pipeline(entrypoint string) (PipelineDescription, bool) {
	for _, pipeline := range d.Pipelines {
		if pipeline.Name == entrypoint {
			return pipeline, true
		}
	}
	return PipelineDescription{}, false
}
