// Package natspod gives the pods the operator creates the NATS connection options the
// installer set once on the chart: a CA bundle, a client certificate, and a credential, each
// from a Secret, mounted at fixed paths and named to the container by the variables in
// pkg/natsconn. With nothing set it changes nothing.
package natspod

import (
	"os"
	"slices"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
)

// The chart sets these on the operator; they name the Secrets and the keys in them.
const (
	EnvCASecret      = "KUBEMOOT_NATS_CA_SECRET"
	EnvCAKey         = "KUBEMOOT_NATS_CA_KEY"
	EnvClientSecret  = "KUBEMOOT_NATS_CLIENT_SECRET"
	EnvAuthSecret    = "KUBEMOOT_NATS_AUTH_SECRET"
	EnvAuthCredsKey  = "KUBEMOOT_NATS_AUTH_CREDS_KEY"
	EnvAuthUserKey   = "KUBEMOOT_NATS_AUTH_USER_KEY"
	EnvAuthPasswdKey = "KUBEMOOT_NATS_AUTH_PASSWORD_KEY"
	EnvAuthTokenKey  = "KUBEMOOT_NATS_AUTH_TOKEN_KEY"
	defaultCAKey     = "ca.crt"
	clientCertKey    = "tls.crt"
	clientKeyKey     = "tls.key"
	mountRoot        = "/etc/kubemoot/nats-conn"
	volumeCA         = "nats-conn-ca"
	volumeClient     = "nats-conn-client"
	volumeAuth       = "nats-conn-auth"
	credsFileName    = "nats.creds"
	dirCA            = mountRoot + "/ca"
	dirClient        = mountRoot + "/client"
	dirAuth          = mountRoot + "/auth"
)

// Source is where the connection options come from.
type Source struct {
	CASecret, CAKey      string
	ClientSecret         string
	AuthSecret           string
	CredsKey             string
	UserKey, PasswordKey string
	TokenKey             string
}

// FromEnv reads the source from the operator's environment.
func FromEnv() Source { return fromGetenv(os.Getenv) }

func fromGetenv(get func(string) string) Source {
	s := Source{
		CASecret: get(EnvCASecret), CAKey: get(EnvCAKey), ClientSecret: get(EnvClientSecret),
		AuthSecret: get(EnvAuthSecret), CredsKey: get(EnvAuthCredsKey),
		UserKey: get(EnvAuthUserKey), PasswordKey: get(EnvAuthPasswdKey), TokenKey: get(EnvAuthTokenKey),
	}
	if s.CAKey == "" {
		s.CAKey = defaultCAKey
	}
	return s
}

// SecretNames lists the Secrets the pods mount, so the operator can copy them into the
// namespaces its workloads run in.
func (s Source) SecretNames() []string {
	var out []string
	for _, n := range []string{s.CASecret, s.ClientSecret, s.AuthSecret} {
		if n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// Apply adds the volumes to the pod and the mounts and variables to each container named
// (init containers included). A container the pod does not have is skipped.
func (s Source) Apply(spec *corev1.PodSpec, containers ...string) {
	volumes := s.volumes()
	if len(volumes) == 0 && !s.hasEnv() {
		return
	}
	spec.Volumes = appendMissingVolumes(spec.Volumes, volumes)
	mounts, env := s.mounts(), s.env()
	for _, list := range []*[]corev1.Container{&spec.InitContainers, &spec.Containers} {
		for i := range *list {
			if slices.Contains(containers, (*list)[i].Name) {
				c := &(*list)[i]
				c.VolumeMounts = appendMissingMounts(c.VolumeMounts, mounts)
				c.Env = appendMissingEnv(c.Env, env)
			}
		}
	}
}

func (s Source) hasEnv() bool { return len(s.env()) > 0 }

func (s Source) volumes() []corev1.Volume {
	var out []corev1.Volume
	if s.CASecret != "" {
		out = append(out, secretVolume(volumeCA, s.CASecret, map[string]string{s.CAKey: defaultCAKey}))
	}
	if s.ClientSecret != "" {
		out = append(out, secretVolume(volumeClient, s.ClientSecret, nil))
	}
	if s.AuthSecret != "" && s.CredsKey != "" {
		out = append(out, secretVolume(volumeAuth, s.AuthSecret, map[string]string{s.CredsKey: credsFileName}))
	}
	return out
}

func (s Source) mounts() []corev1.VolumeMount {
	var out []corev1.VolumeMount
	if s.CASecret != "" {
		out = append(out, corev1.VolumeMount{Name: volumeCA, MountPath: dirCA, ReadOnly: true})
	}
	if s.ClientSecret != "" {
		out = append(out, corev1.VolumeMount{Name: volumeClient, MountPath: dirClient, ReadOnly: true})
	}
	if s.AuthSecret != "" && s.CredsKey != "" {
		out = append(out, corev1.VolumeMount{Name: volumeAuth, MountPath: dirAuth, ReadOnly: true})
	}
	return out
}

func (s Source) env() []corev1.EnvVar {
	var out []corev1.EnvVar
	if s.CASecret != "" {
		out = append(out, corev1.EnvVar{Name: natsconn.EnvCAFile, Value: dirCA + "/" + defaultCAKey})
	}
	if s.ClientSecret != "" {
		out = append(out,
			corev1.EnvVar{Name: natsconn.EnvClientCertFile, Value: dirClient + "/" + clientCertKey},
			corev1.EnvVar{Name: natsconn.EnvClientKeyFile, Value: dirClient + "/" + clientKeyKey})
	}
	if s.AuthSecret == "" {
		return out
	}
	switch {
	case s.CredsKey != "":
		out = append(out, corev1.EnvVar{Name: natsconn.EnvCredsFile, Value: dirAuth + "/" + credsFileName})
	case s.UserKey != "" && s.PasswordKey != "":
		out = append(out, s.secretEnv(natsconn.EnvUser, s.UserKey), s.secretEnv(natsconn.EnvPassword, s.PasswordKey))
	case s.TokenKey != "":
		out = append(out, s.secretEnv(natsconn.EnvToken, s.TokenKey))
	}
	return out
}

func (s Source) secretEnv(name, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: s.AuthSecret}, Key: key,
	}}}
}

// secretVolume projects the named keys (source key to file name), or the whole Secret when
// none are given. The Secret must exist: a pod told to use TLS must not start without it.
func secretVolume(name, secret string, items map[string]string) corev1.Volume {
	src := &corev1.SecretVolumeSource{SecretName: secret}
	for key, path := range items {
		src.Items = append(src.Items, corev1.KeyToPath{Key: key, Path: path})
	}
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{Secret: src}}
}

func appendMissingVolumes(have, add []corev1.Volume) []corev1.Volume {
	for _, v := range add {
		if !slices.ContainsFunc(have, func(h corev1.Volume) bool { return h.Name == v.Name }) {
			have = append(have, v)
		}
	}
	return have
}

func appendMissingMounts(have, add []corev1.VolumeMount) []corev1.VolumeMount {
	for _, m := range add {
		if !slices.ContainsFunc(have, func(h corev1.VolumeMount) bool { return h.Name == m.Name }) {
			have = append(have, m)
		}
	}
	return have
}

func appendMissingEnv(have, add []corev1.EnvVar) []corev1.EnvVar {
	for _, e := range add {
		if !slices.ContainsFunc(have, func(h corev1.EnvVar) bool { return h.Name == e.Name }) {
			have = append(have, e)
		}
	}
	return have
}
