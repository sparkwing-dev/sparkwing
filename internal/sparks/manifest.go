package sparks

type Manifest struct {
	Libraries []Library `yaml:"libraries"`
}

type Library struct {
	Name string `yaml:"name"`

	Source string `yaml:"source"`

	Version string `yaml:"version"`
}
