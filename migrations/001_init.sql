create database if not exists memora
    default character set utf8mb4
    default collate utf8mb4_unicode_ci;

use memora;

create table if not exists journals (
    id         bigint unsigned auto_increment primary key,
    content    text not null,
    created_at datetime not null default current_timestamp
) engine = innodb default charset = utf8mb4;

create table if not exists journal_insights (
    id            bigint unsigned auto_increment primary key,
    journal_id    bigint unsigned not null,
    status        varchar(16) not null default 'pending',
    summary       text null,
    moods         json null,
    events        json null,
    achievements  json null,
    goals         json null,
    gratitude     json null,
    error_message text null,
    created_at    datetime not null default current_timestamp,
    updated_at    datetime not null default current_timestamp on update current_timestamp,
    unique key uk_insight_journal (journal_id),
    constraint fk_insight_journal foreign key (journal_id) references journals (id) on delete cascade
) engine = innodb default charset = utf8mb4;

create table if not exists chat_messages (
    id         bigint unsigned auto_increment primary key,
    role       varchar(16) not null,
    content    text not null,
    created_at datetime not null default current_timestamp,
    key idx_chat_created_at (created_at)
) engine = innodb default charset = utf8mb4;

create table if not exists memories (
    id                bigint unsigned auto_increment primary key,
    content           text not null,
    category          varchar(32) null,
    status            varchar(16) not null default 'active',
    superseded_by_id  bigint unsigned null,
    is_pinned         tinyint(1) not null default 0,
    created_at        datetime not null default current_timestamp,
    updated_at        datetime not null default current_timestamp on update current_timestamp,
    key idx_memory_status_id (status, id)
) engine = innodb default charset = utf8mb4;

create table if not exists memory_sources (
    id              bigint unsigned auto_increment primary key,
    memory_id       bigint unsigned not null,
    journal_id      bigint unsigned null,
    chat_message_id bigint unsigned null,
    created_at      datetime not null default current_timestamp,
    unique key uk_ms_memory_journal (memory_id, journal_id),
    unique key uk_ms_memory_chat (memory_id, chat_message_id),
    constraint fk_ms_memory foreign key (memory_id) references memories (id) on delete cascade,
    constraint fk_ms_journal foreign key (journal_id) references journals (id) on delete restrict,
    constraint fk_ms_chat foreign key (chat_message_id) references chat_messages (id) on delete restrict,
    constraint chk_ms_exactly_one_source check (
        (journal_id is not null and chat_message_id is null) or
        (journal_id is null and chat_message_id is not null)
    )
) engine = innodb default charset = utf8mb4;
