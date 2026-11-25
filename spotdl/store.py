"""La cola de trabajos en SQLite y su forma en JSON para la app."""

import json
import secrets
import sqlite3
import threading
import time


class Store:
    """Cola persistente en SQLite; el servicio es su único dueño."""

    SCHEMA = """
    CREATE TABLE IF NOT EXISTS jobs(
        id TEXT PRIMARY KEY,
        url TEXT NOT NULL,
        title TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL,
        current TEXT NOT NULL DEFAULT '',
        total INTEGER NOT NULL DEFAULT 0,
        done INTEGER NOT NULL DEFAULT 0,
        error TEXT NOT NULL DEFAULT '',
        created INTEGER NOT NULL,
        owner TEXT NOT NULL DEFAULT '',
        attempts INTEGER NOT NULL DEFAULT 1,
        failed TEXT NOT NULL DEFAULT '[]',
        notice TEXT NOT NULL DEFAULT ''
    );
    CREATE INDEX IF NOT EXISTS jobs_created ON jobs(created DESC);
    """
    # Columnas que bases viejas no tienen. Sin dueño, el trabajo solo lo quita el administrador.
    # failed: [{"url", "name", "retry"}]; url vacía con retry = no se sabe cuál, se repite el enlace entero.
    COLUMNS = {
        "owner": "TEXT NOT NULL DEFAULT ''",
        "attempts": "INTEGER NOT NULL DEFAULT 1",
        "failed": "TEXT NOT NULL DEFAULT '[]'",
        "notice": "TEXT NOT NULL DEFAULT ''",
        "songs": "TEXT NOT NULL DEFAULT '[]'",  # playlist: sus canciones en orden, [[artistas], título]
        "tracks": "TEXT NOT NULL DEFAULT '[]'",  # playlist terminada: la ruta de cada canción en la biblioteca
    }

    def __init__(self, path, keep_finished=30, max_attempts=3):
        self.keep = keep_finished
        self.max_attempts = max_attempts
        self.lock = threading.RLock()
        self.db = sqlite3.connect(path, check_same_thread=False, timeout=10)
        self.db.row_factory = sqlite3.Row
        with self.lock:
            self.db.execute("PRAGMA journal_mode=WAL")
            self.db.execute("PRAGMA busy_timeout=5000")
            self.db.executescript(self.SCHEMA)
            cols = [r["name"] for r in self.db.execute("PRAGMA table_info(jobs)")]
            for name, ddl in self.COLUMNS.items():
                if name not in cols:
                    self.db.execute(f"ALTER TABLE jobs ADD COLUMN {name} {ddl}")
            self.db.execute("PRAGMA user_version=1")
            self.db.commit()

    def resume(self):
        """Lo que quedó a medias vuelve a la cola; spotDL salta lo ya descargado."""
        with self.lock:
            # Un apagado entre marcar 'canceled' y borrar la fila la dejaba para siempre.
            self.db.execute("DELETE FROM jobs WHERE status='canceled'")
            self.db.execute(
                "UPDATE jobs SET status='queued', done=0, total=0, current='', error='' "
                "WHERE status IN ('queued', 'downloading')"
            )
            self.db.commit()

    def jobs(self, user=None, admin=False):
        """La cola completa; los avisos (finished) solo para su dueño, o los sin dueño para el admin."""
        with self.lock:
            rows = self.db.execute(
                "SELECT * FROM jobs WHERE status<>'finished' OR owner=? OR (owner='' AND ?) ORDER BY "
                "CASE WHEN status IN ('queued', 'downloading') THEN 0 ELSE 1 END, "
                "created DESC, rowid DESC",
                (user, admin),
            ).fetchall()
        return [job_json(r) for r in rows]

    def failed(self, jid):
        with self.lock:
            row = self.db.execute("SELECT failed FROM jobs WHERE id=?", (jid,)).fetchone()
        return json.loads(row["failed"]) if row else []

    def get(self, jid):
        with self.lock:
            row = self.db.execute("SELECT * FROM jobs WHERE id=?", (jid,)).fetchone()
        return job_json(row) if row else None

    def status(self, jid):
        with self.lock:
            row = self.db.execute("SELECT status FROM jobs WHERE id=?", (jid,)).fetchone()
        return row["status"] if row else None

    def create(self, url, owner=""):
        jid = secrets.token_hex(8)
        with self.lock:
            self.db.execute(
                "INSERT INTO jobs(id, url, title, status, current, total, done, error, created, owner) "
                "VALUES(?, ?, '', 'queued', '', ?, 0, '', ?, ?)",
                (jid, url, 1 if "/track/" in url else 0, int(time.time() * 1000), owner),
            )
            self.db.commit()
        return self.get(jid)

    def active_count(self, owner=None, tracks=None):
        """Trabajos activos; con owner, solo los de ese usuario; con tracks, solo (True) o sin (False) canciones sueltas."""
        sql = "SELECT COUNT(*) FROM jobs WHERE status IN ('queued', 'downloading')"
        args = ()
        if tracks is not None:
            sql += f" AND url {'' if tracks else 'NOT '}LIKE '%/track/%'"
        if owner is not None:
            sql, args = sql + " AND owner=?", (owner,)
        with self.lock:
            return self.db.execute(sql, args).fetchone()[0]

    def find_active(self, url, owner):
        """El mismo enlace ya pedido por ese usuario; el de otro no cuenta: cada quien espera su playlist."""
        with self.lock:
            row = self.db.execute(
                "SELECT * FROM jobs WHERE url=? AND owner=? AND status IN ('queued', 'downloading') "
                "ORDER BY created LIMIT 1",
                (url, owner),
            ).fetchone()
        return job_json(row) if row else None

    def next_queued(self):
        # Las canciones sueltas pasan primero: no esperan detrás de una playlist de horas.
        with self.lock:
            row = self.db.execute(
                "SELECT * FROM jobs WHERE status='queued' "
                "ORDER BY instr(url, '/track/') = 0, created, rowid LIMIT 1"
            ).fetchone()
        return job_json(row) if row else None

    def claim(self, jid):
        """Pasa el trabajo a 'downloading'; False si otro ya lo tomó o se canceló."""
        with self.lock:
            cur = self.db.execute(
                "UPDATE jobs SET status='downloading', done=0, "
                "total=CASE WHEN instr(url, '/track/') > 0 THEN 1 ELSE 0 END, "
                "current='', error='' WHERE id=? AND status='queued'",
                (jid,),
            )
            self.db.commit()
            return cur.rowcount == 1

    def progress(self, jid, feed):
        # Los ceros y los textos vacíos no borran lo que ya se sabía (un track nace con total=1).
        with self.lock:
            self.db.execute(
                "UPDATE jobs SET "
                "title=CASE WHEN ?<>'' THEN ? ELSE title END, "
                "current=CASE WHEN ?<>'' THEN ? ELSE current END, "
                "total=CASE WHEN ?>0 THEN ? ELSE total END, done=? "
                "WHERE id=? AND status='downloading'",
                (feed.title, feed.title, feed.current, feed.current, feed.total, feed.total, feed.done, jid),
            )
            self.db.commit()

    def songs(self, jid):
        with self.lock:
            row = self.db.execute("SELECT songs FROM jobs WHERE id=?", (jid,)).fetchone()
        return json.loads(row["songs"]) if row else []

    def set_songs(self, jid, songs):
        with self.lock:
            self.db.execute("UPDATE jobs SET songs=? WHERE id=?", (json.dumps(songs, ensure_ascii=False), jid))
            self.db.commit()

    def set_status(self, jid, status):
        with self.lock:
            self.db.execute(
                "UPDATE jobs SET status=? WHERE id=? AND status IN ('queued', 'downloading')",
                (status, jid),
            )
            self.db.commit()

    def finish(self, jid, status, error="", failed=(), notice="", title="", tracks=()):
        with self.lock:
            self.db.execute(
                "UPDATE jobs SET status=?, error=?, failed=?, notice=?, tracks=?, "
                "title=CASE WHEN title='' THEN ? ELSE title END, "
                "total=CASE WHEN total=0 AND done>0 THEN done ELSE total END "
                "WHERE id=? AND status='downloading'",
                (status, error, json.dumps(list(failed), ensure_ascii=False), notice,
                 json.dumps(list(tracks), ensure_ascii=False), title, jid),
            )
            self.db.commit()
            self.prune()

    def requeue(self, jid):
        """Vuelve a la cola un trabajo reintentable; conserva la lista de fallidas para pedir solo esas."""
        with self.lock:
            cur = self.db.execute(
                "UPDATE jobs SET status='queued', attempts=attempts+1, error='', done=0, current='' "
                "WHERE id=? AND status='error' AND attempts<?",
                (jid, self.max_attempts),
            )
            self.db.commit()
            return cur.rowcount == 1

    def delete(self, jid):
        with self.lock:
            self.db.execute("DELETE FROM jobs WHERE id=?", (jid,))
            self.db.commit()

    def prune(self):
        """Conserva solo los últimos terminados de cada dueño: los de otros no le borran su aviso."""
        with self.lock:
            self.db.execute(
                "DELETE FROM jobs WHERE status NOT IN ('queued', 'downloading') AND id NOT IN ("
                "  SELECT id FROM (SELECT id, ROW_NUMBER() OVER ("
                "    PARTITION BY owner ORDER BY created DESC, rowid DESC) AS n "
                "  FROM jobs WHERE status NOT IN ('queued', 'downloading')) WHERE n <= ?)",
                (self.keep,),
            )
            self.db.commit()


def job_json(row):
    total, done = row["total"], row["done"]
    progress = round(min(done, total) / total, 4) if total > 0 else None
    return {
        "id": row["id"],
        "url": row["url"],
        "title": row["title"],
        "status": row["status"],
        "current": row["current"],
        "total": total,
        "done": done,
        "progress": progress,
        "error": row["error"],
        "created": row["created"],
        "owner": row["owner"],
        "attempts": row["attempts"],
        "notice": row["notice"],
        # Con una playlist terminada: sus canciones en orden, para que la app la cree a quien la pidió.
        "tracks": json.loads(row["tracks"]) if row["status"] == "finished" else [],
    }
