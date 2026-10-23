#!/usr/bin/env python3
"""Run full application fresh/upgrade/repeated-start checks on three real DBs.
Requires local MySQL root socket access, PostgreSQL root socket access and the
released/current Linux binaries supplied as arguments. Uses isolated test DBs.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import sqlite3

parser = argparse.ArgumentParser()
parser.add_argument('--released', required=True)
parser.add_argument('--current', required=True)
args = parser.parse_args()


def query(engine, database, sql):
    if engine == 'sqlite':
        with sqlite3.connect(database) as db:
            return db.execute(sql).fetchall()
    command = ['mysql', '-N', database, '-e', sql] if engine == 'mysql' else ['psql', '-At', '-d', database, '-c', sql]
    result = subprocess.run(command, check=True, capture_output=True, text=True)
    return [tuple(line.split('\t' if engine == 'mysql' else '|')) for line in result.stdout.splitlines()]


def start(binary, engine, database, log_database, folder, label):
    env = dict(os.environ, GIN_MODE='release', SESSION_SECRET='isolated-migration-verification-secret', CRYPTO_SECRET='isolated-migration-verification-crypto', REDIS_CONN_STRING='', SQLITE_PATH=str(database), SQLITE_BUSY_TIMEOUT='30000')
    env['SQL_DSN'] = {'sqlite': 'local', 'mysql': f'root@unix(/var/run/mysqld/mysqld.sock)/{database}?parseTime=true&charset=utf8mb4', 'postgres': f'postgresql:///{database}?host=/var/run/postgresql'}[engine]
    env['LOG_SQL_DSN'] = '' if engine == 'sqlite' else {'mysql': f'root@unix(/var/run/mysqld/mysqld.sock)/{log_database}?parseTime=true&charset=utf8mb4', 'postgres': f'postgresql:///{log_database}?host=/var/run/postgresql'}[engine]
    log = folder / f'{label}.log'
    with log.open('w') as output:
        process = subprocess.Popen([binary, '--port', '18431', '--log-dir', str(folder / 'logs')], cwd=folder, env=env, stdout=output, stderr=subprocess.STDOUT)
        try:
            for _ in range(300):
                if process.poll() is not None:
                    raise RuntimeError(f'{label} exited {process.returncode}: {log.read_text()[-6000:]}')
                try:
                    with urllib.request.urlopen('http://127.0.0.1:18431/api/status', timeout=1) as response:
                        data = json.load(response)
                    if data.get('success'):
                        # Startup returns only after synchronous database migration.
                        return
                except (OSError, ValueError, urllib.error.URLError):
                    pass
                time.sleep(.2)
            raise RuntimeError(f'{label} did not start: {log.read_text()[-6000:]}')
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


with tempfile.TemporaryDirectory(prefix='new-api-free-full-migration-') as tmp:
    root = Path(tmp)
    for engine in ['sqlite', 'mysql', 'postgres']:
        for scenario in ['fresh', 'upgrade']:
            folder = root / f'{engine}-{scenario}'
            folder.mkdir()
            database = str(folder / 'primary.db') if engine == 'sqlite' else f'new_api_free_runtime_{scenario}'
            log_database = f'{database}_logs'
            if engine == 'mysql':
                subprocess.run(['mysql', '-e', f'DROP DATABASE IF EXISTS {database}; CREATE DATABASE {database} CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; DROP DATABASE IF EXISTS {log_database}; CREATE DATABASE {log_database} CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;'], check=True)
            if engine == 'postgres':
                for name in [database, log_database]:
                    subprocess.run(['dropdb', '--if-exists', name], check=True)
                    subprocess.run(['createdb', name], check=True)
            if scenario == 'upgrade':
                start(args.released, engine, database, log_database, folder, 'released')
                group_col = '"group"' if engine == 'postgres' else '`group`'
                query(engine, database, f"INSERT INTO users(username,password,role,status,{group_col},quota,used_quota,request_count) VALUES ('free_migration_marker','invalid-test-password',1,1,'default',123456,234,7)")
            for iteration in range(2):
                start(args.current, engine, database, log_database, folder, f'current-{iteration+1}')
                config = query(engine, database, 'SELECT version,config FROM free_group_config_records WHERE id=1')
                assert len(config) == 1 and int(config[0][0]) == 1, config
                assert json.loads(config[0][1])['enabled'] is False
                assert int(query(engine, database, 'SELECT COUNT(*) FROM free_group_config_versions')[0][0]) == 1
                if scenario == 'upgrade':
                    row = query(engine, database, "SELECT quota,used_quota,request_count FROM users WHERE username='free_migration_marker'")
                    assert tuple(map(int,row[0])) == (123456,234,7), row
            print(f'PASS {engine} {scenario}: full startup twice; singleton/version preserved; existing user balances preserved', flush=True)
    print('PASS full database matrix (SQLite, MySQL, PostgreSQL; separate MySQL/PostgreSQL log databases)', flush=True)
