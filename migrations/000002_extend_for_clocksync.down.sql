BEGIN;

drop table clocksync_device;

alter table deployment
drop column clocksync_completed_at;

alter table deployment_device
drop column clocksync_completed_at;


COMMIT;
