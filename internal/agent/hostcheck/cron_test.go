package hostcheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCrontab(t *testing.T) {
	Root = t.TempDir()
	defer func() { Root = "/" }()
	os.MkdirAll(p("var", "spool", "cron", "crontabs"), 0o755)
	os.MkdirAll(p("etc"), 0o755)
	os.WriteFile(p("var", "spool", "cron", "crontabs", "deploy"), []byte(`# m h dom mon dow command
MAILTO=""
*/5 * * * * cd /srv/app && php artisan schedule:run >> /dev/null 2>&1
@daily   /opt/backup.sh --all
@reboot /opt/start.sh
`), 0o600)
	os.WriteFile(p("etc", "crontab"), []byte(`SHELL=/bin/sh
17 *	* * *	root	cd / && run-parts --report /etc/cron.hourly
0 4 * * 1-5 www-data /usr/bin/php /var/www/cleanup.php
`), 0o644)
	jobs := crontab(p("var", "spool", "cron", "crontabs", "deploy"), "crontab", "deploy")
	if len(jobs) != 2 || jobs[0].Schedule != "*/5 * * * *" || jobs[0].Command != "cd /srv/app && php artisan schedule:run >> /dev/null 2>&1" ||
		jobs[1].Schedule != "@daily" || jobs[1].User != "deploy" || jobs[0].File != "/var/spool/cron/crontabs/deploy" {
		t.Fatalf("%+v", jobs)
	}
	sys := crontab(p("etc", "crontab"), "cron.d", "")
	if len(sys) != 1 || sys[0].User != "www-data" || sys[0].Command != "/usr/bin/php /var/www/cleanup.php" {
		t.Fatalf("%+v", sys)
	}
	if again := crontab(p("var", "spool", "cron", "crontabs", "deploy"), "crontab", "deploy"); again[0].ID != jobs[0].ID {
		t.Fatal("IDs change between reads")
	}
	r := runLog{"deploy\x00cd /srv/app && php artisan schedule:run >> /dev/null 2>&1": 5, "root\x00/opt/a-very-long-command-that-cron-shortened-in-its-log": 9}
	if r.last("deploy", jobs[0].Command) != 5 || r.last("root", "/opt/a-very-long-command-that-cron-shortened-in-its-log --and more") != 9 {
		t.Fatal("last runs")
	}
	os.Symlink("/usr/share/zoneinfo/Europe/Berlin", filepath.Join(Root, "etc", "localtime"))
	if tz := timeZone(); tz != "Europe/Berlin" && tz != "UTC" { // symlinks may not be allowed (Windows)
		t.Fatal(tz)
	}
}
