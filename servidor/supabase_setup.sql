-- Rift Fighters: base de datos de estadisticas (Supabase, plan gratis)
-- Pegar TODO este archivo en Supabase > SQL Editor > New query > Run.
-- Crea una tabla privada con los reportes que manda el juego y una vista publica con las partidas
-- confirmadas (sin duplicados). La clave publica (anon) solo puede AGREGAR reportes y LEER la vista.

create table if not exists public.reportes (
  id        bigint generated always as identity primary key,
  match_id  text        not null check (char_length(match_id) between 3 and 64),
  lado      smallint    not null check (lado in (1, 2)),
  jugador   text        not null default 'Anonimo' check (char_length(jugador) between 1 and 24),
  version   integer     not null default 0 check (version between 0 and 100000),
  p1        jsonb       not null check (jsonb_typeof(p1) = 'array' and jsonb_array_length(p1) between 1 and 4),
  p2        jsonb       not null check (jsonb_typeof(p2) = 'array' and jsonb_array_length(p2) between 1 and 4),
  ganador   smallint    not null check (ganador in (0, 1, 2)),
  recibido  timestamptz not null default now(),
  unique (match_id, lado)
);

alter table public.reportes enable row level security;

-- la clave publica puede agregar reportes, pero no leer, cambiar ni borrar la tabla
drop policy if exists "agregar reportes" on public.reportes;
create policy "agregar reportes" on public.reportes
  for insert to anon
  with check (octet_length(p1::text) < 2000 and octet_length(p2::text) < 2000);

revoke all on public.reportes from anon, authenticated;
grant insert (match_id, lado, jugador, version, p1, p2, ganador) on public.reportes to anon;

-- Vista publica: una fila por pelea. Si los dos jugadores la reportaron, tienen que coincidir
-- en el ganador; si no coinciden, la pelea no cuenta (posible trampa o error).
create or replace view public.partidas_publicas as
select
  r.match_id,
  min(r.recibido)                                   as fecha,
  max(r.version)                                    as version,
  (array_agg(r.p1 order by r.lado))[1]              as p1,
  (array_agg(r.p2 order by r.lado))[1]              as p2,
  min(r.ganador)                                    as ganador,
  max(case when r.lado = 1 then r.jugador end)      as jugador1,
  max(case when r.lado = 2 then r.jugador end)      as jugador2,
  count(*)                                          as reportes
from public.reportes r
group by r.match_id
having min(r.ganador) = max(r.ganador);

grant select on public.partidas_publicas to anon;

-- Limite de abuso sencillo: mas de 200 reportes en una hora con el mismo apodo se rechazan.
create or replace function public.rf_limite() returns trigger language plpgsql security definer set search_path = public as $$
begin
  if (select count(*) from public.reportes where jugador = new.jugador and recibido > now() - interval '1 hour') > 200 then
    raise exception 'demasiados reportes';
  end if;
  return new;
end $$;
drop trigger if exists rf_limite on public.reportes;
create trigger rf_limite before insert on public.reportes for each row execute function public.rf_limite();
