-- Apps whose process runs from "/" (systemd's default cwd) were given "/" as their
-- folder, and the folder scan then measured the whole disk. Forget both.
UPDATE apps SET path = '' WHERE path = '/';
DELETE FROM app_storage WHERE json_extract(data, '$.path') = '/';
