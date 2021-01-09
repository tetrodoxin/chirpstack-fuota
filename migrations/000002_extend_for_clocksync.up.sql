BEGIN;

create table clocksync_device (
    dev_eui bytea primary key not null,
    created_at timestamp with time zone not null,
    updated_at timestamp with time zone not null,

    last_sync_at timestamp with time zone null
);

alter table deployment
add column clocksync_completed_at timestamp with time zone null;

alter table deployment_device
add column clocksync_completed_at timestamp with time zone null;

COMMIT;
