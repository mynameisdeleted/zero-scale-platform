package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/platform"
	"io"
	"math/big"
	"strings"
)

// GenerateRandomBase64 creates cryptographically secure base64-encoded random bytes.
func GenerateRandomBase64(byteCount int) (string, error) {
	b := make([]byte, byteCount)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// GeneratePassword creates a secure random alphanumeric password.
func GeneratePassword(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			b[i] = charset[i%len(charset)]
		} else {
			b[i] = charset[num.Int64()]
		}
	}
	return string(b)
}

// existingPassword reuses the app's current DB password so re-applying never rotates it.
func existingPassword(app *AppConfig, namespace string) string {
	existingSecret, _ := GetSecretData(app.SecretName(), namespace)
	if app.Database.EnvPassword != "" && existingSecret[app.Database.EnvPassword] != "" {
		return existingSecret[app.Database.EnvPassword]
	}
	if existingSecret["DATABASE_PASSWORD"] != "" {
		return existingSecret["DATABASE_PASSWORD"]
	}
	return GeneratePassword(24)
}

// ProvisionMySQL idempotently provisions a schema and user on the configured MySQL cluster.
func ProvisionMySQL(app *AppConfig, namespace string) (dbName, dbUser, dbPass string, target *platform.DBTarget, err error) {
	target, op, err := resolveDBTarget("mysql", namespace)
	if err != nil {
		return "", "", "", nil, err
	}
	dbPass = existingPassword(app, namespace)
	dbName, dbUser = app.DBName(), app.DBUser()

	sql, err := platform.MySQLProvisionSQL(dbName, dbUser, dbPass)
	if err != nil {
		return "", "", "", nil, err
	}
	fmt.Printf("--> Ensuring MySQL schema '%s' and user on %s (%s)...\n", dbName, target.Ref(), op.Display)
	if err := runMySQL(target, op, sql); err != nil {
		return "", "", "", nil, fmt.Errorf("failed to provision MySQL database: %w", err)
	}
	return dbName, dbUser, dbPass, target, nil
}

// ProvisionPostgres idempotently provisions a database and login role on the configured Postgres cluster.
func ProvisionPostgres(app *AppConfig, namespace string) (dbName, dbUser, dbPass string, target *platform.DBTarget, err error) {
	target, op, err := resolveDBTarget("postgres", namespace)
	if err != nil {
		return "", "", "", nil, err
	}
	dbPass = existingPassword(app, namespace)
	dbName, dbUser = app.DBName(), app.DBUser()

	sql, err := platform.PostgresProvisionSQL(dbName, dbUser, dbPass, app.Owner(namespace))
	if err != nil {
		return "", "", "", nil, err
	}
	fmt.Printf("--> Ensuring PostgreSQL database '%s' and role on %s (%s)...\n", dbName, target.Ref(), op.Display)
	if err := runPostgres(target, op, sql); err != nil {
		return "", "", "", nil, fmt.Errorf("failed to provision PostgreSQL database: %w", err)
	}
	return dbName, dbUser, dbPass, target, nil
}

// ProvisionDataLayer orchestrates database and Redis provisioning and populates k8s resources.
func ProvisionDataLayer(app *AppConfig, namespace string) error {
	secretData := make(map[string]string)
	configMapData := make(map[string]string)

	if app.Database != nil {
		engine := strings.ToLower(app.Database.Engine)
		var dbName, dbUser, dbPass string
		var target *platform.DBTarget
		var err error

		if engine == "mysql" {
			dbName, dbUser, dbPass, target, err = ProvisionMySQL(app, namespace)
		} else if engine == "postgres" || engine == "postgresql" {
			dbName, dbUser, dbPass, target, err = ProvisionPostgres(app, namespace)
		} else {
			return fmt.Errorf("unsupported database engine '%s' (choose 'mysql' or 'postgres')", engine)
		}
		if err != nil {
			return err
		}
		endpoint, hostOnly, portOnly := target.RWService, target.Host(), target.Port()

		// Map environment variables requested by binary
		if app.Database.EnvAddress != "" {
			configMapData[app.Database.EnvAddress] = endpoint
			// Also automatically provide _HOST and _PORT derivations if not explicitly set
			if app.Database.EnvHost == "" {
				configMapData[app.Database.EnvAddress+"_HOST"] = hostOnly
			}
			if app.Database.EnvPort == "" {
				configMapData[app.Database.EnvAddress+"_PORT"] = portOnly
			}
		}
		if app.Database.EnvHost != "" {
			configMapData[app.Database.EnvHost] = hostOnly
			if app.Database.EnvPort == "" {
				configMapData[app.Database.EnvHost+"_PORT"] = portOnly
			}
		}
		if app.Database.EnvPort != "" {
			configMapData[app.Database.EnvPort] = portOnly
		}
		if app.Database.EnvDatabase != "" {
			configMapData[app.Database.EnvDatabase] = dbName
		}
		if app.Database.EnvUser != "" {
			secretData[app.Database.EnvUser] = dbUser
		}
		if app.Database.EnvPassword != "" {
			secretData[app.Database.EnvPassword] = dbPass
			// Also alias _PASSWORD <-> _PASS
			if strings.HasSuffix(app.Database.EnvPassword, "_PASS") {
				secretData[app.Database.EnvPassword+"WORD"] = dbPass
			} else if strings.HasSuffix(app.Database.EnvPassword, "_PASSWORD") {
				secretData[strings.TrimSuffix(app.Database.EnvPassword, "WORD")] = dbPass
			}
		}

		// Standard fallbacks for gateway backward compatibility
		if secretData["DATABASE_USER"] == "" {
			secretData["DATABASE_USER"] = dbUser
		}
		if secretData["DATABASE_PASSWORD"] == "" {
			secretData["DATABASE_PASSWORD"] = dbPass
		}
		if configMapData["DATABASE_HOST"] == "" {
			configMapData["DATABASE_HOST"] = hostOnly
		}
		if configMapData["DATABASE_PORT"] == "" {
			configMapData["DATABASE_PORT"] = portOnly
		}
		if configMapData["DATABASE_NAME"] == "" {
			configMapData["DATABASE_NAME"] = dbName
		}
	}

	if app.Redis != nil {
		redisEndpoint := fmt.Sprintf("shared-redis.%s.svc.cluster.local:6379", namespace)
		if p, err := loadPlatform(); err == nil && p != nil && p.Redis != "" {
			redisEndpoint = p.Redis
		}
		redisHost, redisPort, _ := strings.Cut(redisEndpoint, ":")

		if app.Redis.EnvAddress != "" {
			configMapData[app.Redis.EnvAddress] = redisEndpoint
		}
		if app.Redis.EnvHost != "" {
			configMapData[app.Redis.EnvHost] = redisHost
		}
		if app.Redis.EnvPort != "" {
			configMapData[app.Redis.EnvPort] = redisPort
		}
	}

	// Managed secrets: check cross-service references (e.g. fdm.FLEET_SERVER_PRIVATE_KEY),
	// existing Secret in Kubernetes to preserve existing values, or auto-generate/prompt if missing.
	if len(app.Secrets) > 0 {
		existingSecret, _ := GetSecretData(app.SecretName(), namespace)
		for k, genRule := range app.Secrets {
			rule := strings.TrimSpace(genRule)

			// 1. Cross-service reference: <service>.<KEY> (e.g. fdm.FLEET_SERVER_PRIVATE_KEY)
			if strings.Contains(rule, ".") && !strings.Contains(rule, " ") && !strings.HasPrefix(rule, "http") {
				parts := strings.SplitN(rule, ".", 2)
				refApp := strings.ToLower(strings.TrimSpace(parts[0]))
				refKey := strings.TrimSpace(parts[1])
				if refApp != "" && refKey != "" {
					var resolvedVal string
					if refSec, err := GetSecretData(fmt.Sprintf("%s-db-secret", refApp), namespace); err == nil && refSec[refKey] != "" {
						resolvedVal = refSec[refKey]
					} else if refCM, err := GetConfigMapData(fmt.Sprintf("%s-db-config", refApp), namespace); err == nil && refCM[refKey] != "" {
						resolvedVal = refCM[refKey]
					} else if refAppCM, err := GetConfigMapData(fmt.Sprintf("zero-scale-app-%s", refApp), namespace); err == nil && refAppCM[refKey] != "" {
						resolvedVal = refAppCM[refKey]
					}
					if resolvedVal == "" {
						return fmt.Errorf("failed to resolve secret '%s' from reference '%s': key '%s' not found in service '%s' secrets/configmaps in namespace '%s'",
							k, rule, refKey, refApp, namespace)
					}
					secretData[k] = resolvedVal
					fmt.Printf("--> Resolved secret '%s' from zero-scale service '%s' (%s).\n", k, refApp, rule)
					continue
				}
			}

			// 2. Preserve existing value if already present in Kubernetes Secret (or migrating from app ConfigMap)
			if existingVal, exists := existingSecret[k]; exists && existingVal != "" {
				secretData[k] = existingVal
				continue
			}
			if existingAppCM, err := GetConfigMapData(fmt.Sprintf("zero-scale-app-%s", app.AppName()), namespace); err == nil {
				if existingVal := existingAppCM[k]; existingVal != "" && existingVal != "generate" {
					secretData[k] = existingVal
					continue
				}
			}

			// 3. Generate or prompt for new secret
			switch strings.ToLower(rule) {
			case "generate", "generate_base64_32", "base64", "base64_32":
				val, err := GenerateRandomBase64(32)
				if err != nil {
					return fmt.Errorf("failed generating random secret for '%s': %w", k, err)
				}
				secretData[k] = val
				fmt.Printf("--> Generated new 32-byte base64 secret for '%s' in Kubernetes Secret '%s'.\n", k, app.SecretName())
			case "password", "generate_password", "generate_password_24":
				val := GeneratePassword(24)
				secretData[k] = val
				fmt.Printf("--> Generated new random password for '%s' in Kubernetes Secret '%s'.\n", k, app.SecretName())
			case "generate_password_32":
				val := GeneratePassword(32)
				secretData[k] = val
				fmt.Printf("--> Generated new random password for '%s' in Kubernetes Secret '%s'.\n", k, app.SecretName())
			case "ask", "prompt":
				val, err := readSecretLine(fmt.Sprintf("Enter secret value for '%s' (hidden): ", k))
				if err != nil || val == "" {
					return fmt.Errorf("secret '%s' is required", k)
				}
				secretData[k] = val
			default:
				secretData[k] = rule
			}
		}
	}

	labels := map[string]string{
		"zero-scale-db": "true",
		"app":           app.AppName(),
	}

	if len(secretData) > 0 {
		err := CreateOrUpdateSecret(app.SecretName(), namespace, secretData, labels)
		if err != nil {
			return fmt.Errorf("failed to create secret %s: %w", app.SecretName(), err)
		}
		fmt.Printf("--> Applied Kubernetes Secret '%s'.\n", app.SecretName())
	}

	if len(configMapData) > 0 {
		err := CreateOrUpdateConfigMap(app.ConfigMapName(), namespace, configMapData, labels)
		if err != nil {
			return fmt.Errorf("failed to create configmap %s: %w", app.ConfigMapName(), err)
		}
		fmt.Printf("--> Applied Kubernetes ConfigMap '%s'.\n", app.ConfigMapName())
	}

	return nil
}
