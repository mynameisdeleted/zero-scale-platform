package main

import (
	"flag"
	"fmt"
	"strings"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/appname"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/platform"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/volumes"
)

func runDeleteApp(args []string, namespace string) error {
	name, rest := splitHostArgs(args)
	fs := flag.NewFlagSet("delete-app", flag.ExitOnError)
	purge := fs.Bool("purge", false, "Also remove the Ingress, Secrets, DB config, host symlinks and OAuth routes, and stop the app on every replica")
	dropDB := fs.Bool("drop-database", false, "With --purge: drop the app's database and user (Postgres only drops databases this tool created)")
	delVols := fs.Bool("delete-volumes", false, "With --purge: delete the app's persistent volume data under /srv/zero-scale-volumes")
	_ = fs.Parse(rest)
	if name == "" {
		return fmt.Errorf("usage: zero-scale-ctl delete-app <name|host> [--purge [--drop-database] [--delete-volumes]]")
	}
	if (*dropDB || *delVols) && !*purge {
		return fmt.Errorf("--drop-database and --delete-volumes require --purge")
	}

	cmName := name
	if !strings.HasPrefix(cmName, "zero-scale-app-") {
		cmName = appname.ConfigMapName(name)
	}
	data, cmErr := GetConfigMapData(cmName, namespace)
	appName := strings.TrimPrefix(cmName, "zero-scale-app-")

	if !*purge {
		if cmErr != nil {
			return fmt.Errorf("ConfigMap '%s' not found in namespace '%s'", cmName, namespace)
		}
		fmt.Printf("==> Deleting ConfigMap '%s' in namespace '%s'...\n", cmName, namespace)
		if _, err := RunKubectl([]string{"delete", "configmap", cmName, "-n", namespace}, ""); err != nil {
			return err
		}
		fmt.Println("Done. The Ingress, Secrets, database and binary were kept; use --purge to remove them.")
		return nil
	}

	hosts := appname.Hosts(data)
	if len(hosts) == 0 && strings.Contains(name, ".") {
		hosts = []string{strings.ToLower(name)}
	}
	fmt.Printf("==> Purging app '%s' (hosts %v) in namespace '%s'\n", appName, hosts, namespace)
	del := func(kind, obj string) {
		if _, err := RunKubectl([]string{"delete", kind, obj, "-n", namespace, "--ignore-not-found"}, ""); err != nil {
			fmt.Printf("⚠️  deleting %s/%s: %v\n", kind, obj, err)
		} else {
			fmt.Printf("--> Deleted %s/%s\n", kind, obj)
		}
	}

	if len(hosts) > 0 {
		stopAppEverywhere(hosts[0], namespace)
		fmt.Printf("--> Stopped app-%s on every gateway replica\n", hosts[0])
		if pod, err := GetGatewayPod(namespace); err == nil {
			for _, h := range hosts {
				if err := platform.ValidateHost(h); err != nil {
					continue
				}
				_, _ = ExecPod(pod, namespace, "systemd-runner", []string{"rm", "-f", "/opt/apps/" + h, "/opt/apps/" + h + ".type"}, "")
			}
			fmt.Println("--> Removed host entries from /opt/apps (the binary itself is kept; other apps may share it)")
		}
	}

	if *delVols {
		if !appname.Valid(appName) {
			return fmt.Errorf("refusing to delete volumes for unexpected app name %q", appName)
		}
		pod, err := GetGatewayPod(namespace)
		if err != nil {
			return fmt.Errorf("deleting volumes: %w", err)
		}
		dir := volumes.AppDir(volumes.DefaultRoot, appName)
		if _, err := ExecPod(pod, namespace, "systemd-runner", []string{"rm", "-rf", "--one-file-system", dir}, ""); err != nil {
			return fmt.Errorf("deleting %s: %w", dir, err)
		}
		fmt.Printf("--> Deleted volume data %s\n", dir)
	} else if data["volumes"] != "" {
		fmt.Printf("--> Kept volume data under %s (use --delete-volumes to remove it)\n", volumes.AppDir(volumes.DefaultRoot, appName))
	}

	if *dropDB {
		if err := dropAppDatabase(appName, hosts, data, namespace); err != nil {
			return err
		}
	}

	if setup := data["oauth-setup"]; setup != "" && len(hosts) > 0 {
		del("ingress", fmt.Sprintf("zero-scale-%s-oauth2", strings.Split(hosts[0], ".")[0]))
		removeOAuthCallbacks(setup, hosts, namespace)
	}
	del("ingress", "zero-scale-"+appName)
	del("secret", appName+"-db-secret")
	del("configmap", appName+"-db-config")
	del("configmap", cmName)
	fmt.Println("✅ Purged.")
	return nil
}

func removeOAuthCallbacks(setup string, hosts []string, namespace string) {
	cmName := OAuthSetupConfigMapName(setup)
	cm, err := GetConfigMapData(cmName, namespace)
	if err != nil {
		return
	}
	drop := map[string]bool{}
	for _, h := range hosts {
		drop[fmt.Sprintf("https://%s/oauth2/callback", h)] = true
	}
	var keep []string
	for _, u := range strings.Split(cm["redirect_uris"], ",") {
		if u = strings.TrimSpace(u); u != "" && !drop[u] {
			keep = append(keep, u)
		}
	}
	cm["redirect_uris"] = strings.Join(keep, ",")
	if err := CreateOrUpdateConfigMap(cmName, namespace, cm, map[string]string{"app.kubernetes.io/part-of": "zero-scale-platform"}); err == nil {
		fmt.Printf("--> Removed %d redirect URI(s) from OAuth setup '%s' (also remove them at the provider)\n", len(hosts), setup)
	}
}

func dropAppDatabase(appName string, hosts []string, data map[string]string, namespace string) error {
	if len(hosts) == 0 {
		return fmt.Errorf("cannot drop the database: the app's hosts are unknown (ConfigMap missing)")
	}
	engine := data["database_engine"]
	if engine == "" {
		// Apps applied before database_engine was recorded: infer from the port.
		if dbCM, err := GetConfigMapData(appName+"-db-config", namespace); err == nil {
			engine = map[string]string{"5432": "postgres", "3306": "mysql"}[dbCM["DATABASE_PORT"]]
		}
	}
	if engine == "" {
		fmt.Println("--> No database recorded for this app; nothing to drop.")
		return nil
	}
	if engine == "postgresql" {
		engine = "postgres"
	}
	app := &AppConfig{Hostnames: hosts}
	target, op, err := resolveDBTarget(engine, namespace)
	if err != nil {
		return err
	}
	switch engine {
	case "postgres":
		sql, err := platform.PostgresDropSQL(app.DBName(), app.DBUser(), app.Owner(namespace))
		if err != nil {
			return err
		}
		if err := runPostgres(target, op, sql); err != nil {
			return fmt.Errorf("dropping Postgres database: %w", err)
		}
		fmt.Printf("--> Dropped Postgres database %s and role %s on %s if this tool created them\n", app.DBName(), app.DBUser(), target.Ref())
	case "mysql":
		sql, err := platform.MySQLDropSQL(app.DBName(), app.DBUser())
		if err != nil {
			return err
		}
		if err := runMySQL(target, op, sql); err != nil {
			return fmt.Errorf("dropping MySQL database: %w", err)
		}
		fmt.Printf("--> Dropped MySQL database %s and user %s on %s\n", app.DBName(), app.DBUser(), target.Ref())
	}
	return nil
}
