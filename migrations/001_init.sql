-- Memora 初始表结构
-- 执行方式：在 MySQL 客户端 / 数据库工具里，整个文件跑一遍即可（可重复执行）。

create database if not exists memora
    default character set utf8mb4
    default collate utf8mb4_unicode_ci;

use memora;

-- 1. 原始日记（事实源，永不覆盖）
create table if not exists journals (
    id         bigint unsigned auto_increment primary key,
    content    text not null,
    created_at datetime not null default current_timestamp
) engine = innodb default charset = utf8mb4;

-- 2. 单篇日记的结构化理解（1:1）
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

-- 3. 长期记忆单元（1:N，0..N 条）
create table if not exists memories (
    id                bigint unsigned auto_increment primary key,
    source_journal_id bigint unsigned not null,
    content           text not null,
    category          varchar(32) null,
    status            varchar(16) not null default 'active',
    superseded_by_id  bigint unsigned null,
    is_pinned         tinyint(1) not null default 0,
    created_at        datetime not null default current_timestamp,
    updated_at        datetime not null default current_timestamp on update current_timestamp,
    key idx_memory_source (source_journal_id),
    constraint fk_memory_journal foreign key (source_journal_id) references journals (id) on delete cascade
) engine = innodb default charset = utf8mb4;
