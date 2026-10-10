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
    id                   bigint unsigned auto_increment primary key,
    role                 varchar(16) not null,
    content              text not null,
    client_id            varchar(64) null,     -- 一次发送的幂等标识；仅 user 行有值，重试复用同一个
    reply_to_id          bigint unsigned null,
    status               varchar(16) not null default 'ok',
    formation_status     varchar(16) not null default 'none', -- none | pending | running | applied | failed
    formation_owner      varchar(64) null,
    formation_started_at datetime null, 
    created_at           datetime not null default current_timestamp,
    updated_at           datetime not null default current_timestamp on update current_timestamp,
    key idx_chat_created_at (created_at),
    key idx_chat_formation_lease (formation_status, formation_started_at),
    unique key uk_chat_client_id (client_id),
    unique key uk_chat_reply_to (reply_to_id)
) engine = innodb default charset = utf8mb4;

create table if not exists memories (
    id               bigint unsigned auto_increment primary key,
    content          text not null,
    category         varchar(32) null,
    status           varchar(16) not null default 'active',
    origin           varchar(16) not null default 'auto',
    superseded_by_id bigint unsigned null,
    is_pinned        tinyint(1) not null default 0,
    created_at       datetime not null default current_timestamp,
    updated_at       datetime not null default current_timestamp on update current_timestamp,
    key idx_memory_status_id (status, id)
) engine = innodb default charset = utf8mb4;

create table if not exists memory_corrections (
    id            bigint unsigned auto_increment primary key,
    memory_id     bigint unsigned not null,
    new_memory_id bigint unsigned null,      -- 纠正产生的新条目；忘记时为空
    action        varchar(16) not null,
    old_content   text not null,
    new_content   text null,
    created_at    datetime not null default current_timestamp,
    key idx_mc_memory (memory_id),
    key idx_mc_new_memory (new_memory_id),
    constraint fk_mc_memory foreign key (memory_id) references memories (id) on delete restrict,
    constraint fk_mc_new_memory foreign key (new_memory_id) references memories (id) on delete set null,
    constraint chk_mc_action check (action in ('correct', 'forget'))
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

create table if not exists life_records (
    id              bigint unsigned auto_increment primary key,
    kind            varchar(16) not null,
    state           varchar(16) not null,
    basis           varchar(16) not null, -- inferred 只用于 mood
    subject         varchar(16) not null,
    content         varchar(500) not null,
    chat_message_id bigint unsigned not null,
    evidence_quote  varchar(255) not null,
    mentioned_on    date not null,        -- 这句话是哪一天说的
    occurred_on     date null,            -- 事情发生在哪一天；不知道就为空
    occurred_month  tinyint unsigned null,
    occurred_day    tinyint unsigned null,
    date_precision  varchar(16) not null default 'unknown',
    date_text       varchar(64) null,
    status          varchar(16) not null default 'active',
    corrected_by_id bigint unsigned null,
    created_at      datetime not null default current_timestamp,
    updated_at      datetime not null default current_timestamp on update current_timestamp,
    key idx_life_occurred (occurred_on, kind),
    key idx_life_mentioned (mentioned_on, kind),
    key idx_life_status_id (status, id),
    constraint fk_life_chat foreign key (chat_message_id) references chat_messages (id) on delete restrict,
    constraint fk_life_corrected_by foreign key (corrected_by_id) references life_records (id) on delete set null,
    constraint chk_life_date_precision check (
        (date_precision = 'day'       and occurred_on is not null and occurred_month is null     and occurred_day is null)
     or (date_precision = 'month_day' and occurred_on is null     and occurred_month is not null and occurred_day is not null)
     or (date_precision = 'unknown'   and occurred_on is null     and occurred_month is null     and occurred_day is null)
    ),
    constraint chk_life_kind_state check (
        (kind = 'activity'        and state in ('done', 'planned', 'negated'))
     or (kind = 'goal'            and state in ('wish', 'planned'))
     or (kind in ('mood', 'date') and state = 'none')
    )
) engine = innodb default charset = utf8mb4;

create table if not exists day_states (
    day                date not null primary key,
    timezone           varchar(64) not null default '',
    label              varchar(32) not null default '',
    summary            varchar(500) not null default '',
    result_digest      char(64) not null default '',         -- 空串表示还没有结果
    input_truncated    tinyint(1) not null default 0,
    manual_label       varchar(32) not null default '',      -- 非空表示用户手动设置，优先于自动结果
    attempt_status     varchar(16) not null default 'none',
    attempt_owner      varchar(64) null,
    attempt_digest     char(64) not null default '',
    attempt_started_at datetime null,
    error_message      varchar(255) not null default '',
    created_at         datetime not null default current_timestamp,
    updated_at         datetime not null default current_timestamp on update current_timestamp,
    key idx_day_state_attempt (attempt_status, attempt_started_at)
) engine = innodb default charset = utf8mb4;
