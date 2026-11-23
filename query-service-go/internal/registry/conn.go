package registry

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultHost   = "localhost"
	defaultPort   = 5432
	defaultDBName = "vectors"
	defaultUser   = "postgres"
)

// Env holds the process-wide database defaults, the KUBEMOOT_DB_* variables the
// operator sets from the vector store's Secret.
type Env struct {
	User     string
	Password string
	DBName   string
}

// EnvFromProcess reads the defaults from the environment.
func EnvFromProcess() Env {
	return Env{
		User:     os.Getenv("KUBEMOOT_DB_USER"),
		Password: os.Getenv("KUBEMOOT_DB_PASSWORD"),
		DBName:   os.Getenv("KUBEMOOT_DB_NAME"),
	}
}

// Conn is a resolved PostgreSQL connection.
type Conn struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	DBName   string `json:"dbname"`
	User     string `json:"user"`
	Password string `json:"-"`
}

// String describes the connection without its password, for logs.
func (c Conn) String() string {
	return fmt.Sprintf("%s@%s:%d/%s", c.User, c.Host, c.Port, c.DBName)
}

// URL is the connection string for the driver, with the user and password escaped.
func (c Conn) URL() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Host, c.Port),
		Path:   "/" + c.DBName,
	}
	return u.String()
}

// ResolveConn turns a source's endpoint into a connection. Accepted forms:
// postgresql://user:pass@host:5432/db, postgres://..., host:port, host:port/db, host.
// Credentials come from the Secret directory when the source names one, else from the
// URL's user info, else from the process defaults.
func ResolveConn(vs VectorStore, env Env) (Conn, error) {
	conn, err := parseEndpoint(vs.Endpoint, env)
	if err != nil {
		return Conn{}, err
	}
	if vs.CredentialsDir != "" {
		return withCredentials(conn, vs.CredentialsDir)
	}
	return conn, nil
}

func parseEndpoint(endpoint string, env Env) (Conn, error) {
	switch {
	case strings.HasPrefix(endpoint, "postgresql://"), strings.HasPrefix(endpoint, "postgres://"):
		return parseURL(endpoint)
	case strings.Contains(endpoint, ":"):
		return parseHostPort(endpoint, env)
	default:
		return envConn(endpoint, defaultPort, env.DBName, env), nil
	}
}

func parseURL(endpoint string) (Conn, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return Conn{}, fmt.Errorf("vector store endpoint: %w", err)
	}
	conn := Conn{
		Host:     orDefault(u.Hostname(), defaultHost),
		Port:     defaultPort,
		DBName:   orDefault(strings.TrimLeft(u.Path, "/"), defaultDBName),
		User:     orDefault(u.User.Username(), defaultUser),
		Password: "",
	}
	conn.Password, _ = u.User.Password()
	if p := u.Port(); p != "" {
		if conn.Port, err = strconv.Atoi(p); err != nil {
			return Conn{}, fmt.Errorf("vector store endpoint port %q: %w", p, err)
		}
	}
	return conn, nil
}

func parseHostPort(endpoint string, env Env) (Conn, error) {
	host, rest, _ := strings.Cut(endpoint, ":")
	portText, db, _ := strings.Cut(rest, "/")
	port, err := strconv.Atoi(portText)
	if err != nil {
		return Conn{}, fmt.Errorf("vector store endpoint port %q: %w", portText, err)
	}
	return envConn(host, port, db, env), nil
}

func envConn(host string, port int, db string, env Env) Conn {
	return Conn{
		Host:     host,
		Port:     port,
		DBName:   orDefault(db, orDefault(env.DBName, defaultDBName)),
		User:     orDefault(env.User, defaultUser),
		Password: env.Password,
	}
}

// withCredentials reads username and password files from a mounted Secret. A missing
// username keeps the one already resolved (the Secret's username key is optional);
// the password file must exist.
func withCredentials(conn Conn, dir string) (Conn, error) {
	pass, err := os.ReadFile(filepath.Join(dir, "password"))
	if err != nil {
		return Conn{}, fmt.Errorf("credentials: %w", err)
	}
	conn.Password = strings.TrimRight(string(pass), "\r\n")
	if user, err := os.ReadFile(filepath.Join(dir, "username")); err == nil {
		conn.User = orDefault(strings.TrimSpace(string(user)), conn.User)
	}
	return conn, nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
