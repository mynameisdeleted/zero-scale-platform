package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
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

// GetMySQLPod returns the running shared-mysql pod name.
func GetMySQLPod(namespace string) (string, error) {
	out, err := RunKubectl([]string{
		"get", "pods", "-n", namespace,
		"-l", "app=shared-mysql",
		"--field-selector=status.phase=Running",
		"-o", "jsonpath={.items[0].metadata.name}",
	}, "")
	pod := strings.TrimSpace(out)
	if err != nil || pod == "" {
		return "", fmt.Errorf(`shared-mysql operator pod is not running in namespace '%s'
--> Remediation: Deploy shared MySQL with: kubectl apply -f deploy/shared-data-services.yaml -n %s
--> Documentation: https://git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform#mysql-and-redis`, namespace, namespace)
	}
	return pod, nil
}

// GetPostgresPod returns the primary CloudNativePG ha-postgres pod name.
func GetPostgresPod(namespace string) (string, error) {
	for _, ns := range []string{namespace, "cnpg-system"} {
		out, err := RunKubectl([]string{
			"get", "pods", "-n", ns,
			"-l", "cnpg.io/cluster=ha-postgres,role=primary",
			"--field-selector=status.phase=Running",
			"-o", "jsonpath={.items[0].metadata.name}",
		}, "")
		pod := strings.TrimSpace(out)
		if err == nil && pod != "" {
			return pod, nil
		}
	}
	return "", fmt.Errorf(`CloudNativePG PostgreSQL cluster ('ha-postgres') is not running in namespace '%s' or 'cnpg-system'
--> Remediation:
    1. Deploy CloudNativePG via Open Cream Cheese:
       https://git.fairfaxmedia.net/open-cream-cheese/open-cream-cheese
    2. Or install directly:
       kubectl apply --server-side -f https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/main/releases/cnpg-1.22.0.yaml
--> Documentation: https://git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform#postgresql-cloudnativepg`, namespace)
}

// ProvisionMySQL idempotently provisions schema and user on shared MySQL operator.
func ProvisionMySQL(app *AppConfig, namespace string) (dbName, dbUser, dbPass, endpoint string, err error) {
	pod, err := GetMySQLPod(namespace)
	if err != nil {
		return "", "", "", "", err
	}

	adminData, err := GetSecretData("shared-mysql-admin", namespace)
	var rootPass string
	if err != nil || adminData["MYSQL_ROOT_PASSWORD"] == "" {
		// Auto-generate high-entropy root password and persist to Kubernetes Secret
		rootPass = GeneratePassword(24)
		if err := CreateOrUpdateSecret("shared-mysql-admin", namespace, map[string]string{
			"MYSQL_ROOT_PASSWORD": rootPass,
		}, map[string]string{
			"app": "shared-mysql",
			"app.kubernetes.io/part-of": "zero-scale-platform",
		}); err != nil {
			return "", "", "", "", fmt.Errorf("failed to auto-create 'shared-mysql-admin' secret: %w", err)
		}
	} else {
		rootPass = adminData["MYSQL_ROOT_PASSWORD"]
	}

	// Reuse existing password from app Secret if already created
	existingSecret, _ := GetSecretData(app.SecretName(), namespace)
	if app.Database.EnvPassword != "" && existingSecret[app.Database.EnvPassword] != "" {
		dbPass = existingSecret[app.Database.EnvPassword]
	} else if existingSecret["DATABASE_PASSWORD"] != "" {
		dbPass = existingSecret["DATABASE_PASSWORD"]
	} else {
		dbPass = GeneratePassword(24)
	}

	dbName = app.DBName()
	dbUser = app.DBUser()

	// Check if database exists
	checkSQL := fmt.Sprintf("SELECT SCHEMA_NAME FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME = '%s';", dbName)
	checkOut, _ := ExecPod(pod, namespace, "", []string{
		"mysql", "-u", "root", fmt.Sprintf("-p%s", rootPass), "-N", "-s", "-e", checkSQL,
	}, "")

	if strings.Contains(checkOut, dbName) {
		fmt.Printf("--> Database schema '%s' already exists on %s.\n", dbName, pod)
	} else {
		fmt.Printf("--> Creating MySQL database schema '%s' on %s...\n", dbName, pod)
		createSQL := fmt.Sprintf(`
CREATE DATABASE IF NOT EXISTS `+"`%s`"+` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s';
ALTER USER '%s'@'%%' IDENTIFIED BY '%s';
GRANT ALL PRIVILEGES ON `+"`%s`"+`.* TO '%s'@'%%';
FLUSH PRIVILEGES;
`, dbName, dbUser, dbPass, dbUser, dbPass, dbName, dbUser)
		_, err = ExecPod(pod, namespace, "", []string{
			"mysql", "-u", "root", fmt.Sprintf("-p%s", rootPass), "-e", createSQL,
		}, "")
		if err != nil {
			return "", "", "", "", fmt.Errorf("failed to create database on MySQL: %w", err)
		}
	}

	endpoint = fmt.Sprintf("shared-mysql.%s.svc.cluster.local:3306", namespace)
	return dbName, dbUser, dbPass, endpoint, nil
}

// ProvisionPostgres idempotently provisions schema and user on CloudNativePG operator.
func ProvisionPostgres(app *AppConfig, namespace string) (dbName, dbUser, dbPass, endpoint string, err error) {
	pod, err := GetPostgresPod(namespace)
	if err != nil {
		return "", "", "", "", err
	}

	existingSecret, _ := GetSecretData(app.SecretName(), namespace)
	if app.Database.EnvPassword != "" && existingSecret[app.Database.EnvPassword] != "" {
		dbPass = existingSecret[app.Database.EnvPassword]
	} else if existingSecret["DATABASE_PASSWORD"] != "" {
		dbPass = existingSecret["DATABASE_PASSWORD"]
	} else {
		dbPass = GeneratePassword(24)
	}

	dbName = app.DBName()
	dbUser = app.DBUser()

	createRoleSQL := fmt.Sprintf(`
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = '%s') THEN
    CREATE ROLE "%s" WITH LOGIN PASSWORD '%s';
  ELSE
    ALTER ROLE "%s" WITH PASSWORD '%s';
  END IF;
END
$$;
`, dbUser, dbUser, dbPass, dbUser, dbPass)

	_, err = ExecPod(pod, namespace, "postgres", []string{
		"psql", "-U", "postgres", "-c", createRoleSQL,
	}, "")
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to create Postgres role: %w", err)
	}

	checkSQL := fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%s';", dbName)
	checkOut, _ := ExecPod(pod, namespace, "postgres", []string{
		"psql", "-U", "postgres", "-tc", checkSQL,
	}, "")

	if strings.Contains(checkOut, "1") {
		fmt.Printf("--> PostgreSQL database '%s' already exists on %s.\n", dbName, pod)
	} else {
		fmt.Printf("--> Creating PostgreSQL database '%s' on %s...\n", dbName, pod)
		_, err = ExecPod(pod, namespace, "postgres", []string{
			"psql", "-U", "postgres", "-c", fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s";`, dbName, dbUser),
		}, "")
		if err != nil {
			return "", "", "", "", fmt.Errorf("failed to create Postgres database: %w", err)
		}
	}

	endpoint = fmt.Sprintf("ha-postgres-rw.%s.svc.cluster.local:5432", namespace)
	return dbName, dbUser, dbPass, endpoint, nil
}

// ProvisionDataLayer orchestrates database and Redis provisioning and populates k8s resources.
func ProvisionDataLayer(app *AppConfig, namespace string) error {
	secretData := make(map[string]string)
	configMapData := make(map[string]string)

	if app.Database != nil {
		engine := strings.ToLower(app.Database.Engine)
		var dbName, dbUser, dbPass, endpoint, hostOnly, portOnly string
		var err error

		if engine == "mysql" {
			dbName, dbUser, dbPass, endpoint, err = ProvisionMySQL(app, namespace)
			if err != nil {
				return err
			}
			hostOnly = fmt.Sprintf("shared-mysql.%s.svc.cluster.local", namespace)
			portOnly = "3306"
		} else if engine == "postgres" || engine == "postgresql" {
			dbName, dbUser, dbPass, endpoint, err = ProvisionPostgres(app, namespace)
			if err != nil {
				return err
			}
			hostOnly = fmt.Sprintf("ha-postgres-rw.%s.svc.cluster.local", namespace)
			portOnly = "5432"
		} else {
			return fmt.Errorf("unsupported database engine '%s' (choose 'mysql' or 'postgres')", engine)
		}

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
		redisHost := fmt.Sprintf("shared-redis.%s.svc.cluster.local", namespace)
		redisPort := "6379"

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
