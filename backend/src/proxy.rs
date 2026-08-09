use std::collections::HashMap;
use std::io;
use std::net::{IpAddr, SocketAddr};
use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Instant;

use axum::http::Uri;
use base64::Engine;
use serde::{Deserialize, Serialize};
use socket2::SockRef;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{lookup_host, TcpListener, TcpSocket, TcpStream};
use tokio::sync::{watch, Mutex, RwLock};
use tokio::task::JoinHandle;
use tracing::{info, warn};
use zbus::Connection;

use crate::modem_manager::{find_modem_path_by_id, modem_network_interface_by_id};

const MAX_HTTP_HEADER_BYTES: usize = 64 * 1024;
const MAX_SOCKS_METHODS: usize = 32;
const PROXY_CONFIG_VERSION: u32 = 1;
static NEXT_PROXY_ID: AtomicU64 = AtomicU64::new(1);

#[derive(Debug, Clone, Copy, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum ProxyProtocol {
    #[default]
    Socks5,
    Http,
}

impl ProxyProtocol {
    fn as_str(self) -> &'static str {
        match self {
            Self::Socks5 => "socks5",
            Self::Http => "http",
        }
    }
}

#[derive(Debug, Clone, Deserialize)]
pub struct ProxyUpsertRequest {
    pub name: String,
    pub protocol: ProxyProtocol,
    pub listen_host: String,
    pub listen_port: u16,
    pub modem_id: String,
    #[serde(default)]
    pub username: Option<String>,
    #[serde(default)]
    pub password: Option<String>,
    #[serde(default)]
    pub enabled: bool,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
struct StoredProxyConfig {
    id: String,
    name: String,
    protocol: ProxyProtocol,
    listen_host: String,
    listen_port: u16,
    modem_id: String,
    #[serde(default)]
    username: String,
    #[serde(default)]
    password: String,
    #[serde(default)]
    enabled: bool,
}

#[derive(Debug, Default, Deserialize, Serialize)]
struct ProxyConfigFile {
    #[serde(default)]
    version: u32,
    #[serde(default)]
    proxies: Vec<StoredProxyConfig>,
}

#[derive(Debug, Clone, Default, Serialize)]
pub struct ProxyStatus {
    pub id: String,
    pub name: String,
    pub protocol: String,
    pub listen_host: String,
    pub listen_port: u16,
    pub modem_id: String,
    pub network_interface: String,
    pub username: String,
    pub has_password: bool,
    pub enabled: bool,
    pub running: bool,
    pub total_connections: u64,
    pub active_connections: u64,
    pub bytes_uploaded: u64,
    pub bytes_downloaded: u64,
    pub errors: u64,
    pub uptime_seconds: u64,
    pub last_error: String,
}

#[derive(Debug, Default, Serialize)]
pub struct ProxyListResponse {
    pub proxies: Vec<ProxyStatus>,
}

#[derive(Default)]
struct ProxyStats {
    total_connections: AtomicU64,
    active_connections: AtomicU64,
    bytes_uploaded: AtomicU64,
    bytes_downloaded: AtomicU64,
    errors: AtomicU64,
}

struct ProxyRuntime {
    network_interface: String,
    started_at: Instant,
    stats: Arc<ProxyStats>,
    stop: watch::Sender<bool>,
    task: JoinHandle<()>,
}

struct ProxyEntry {
    config: StoredProxyConfig,
    runtime: Option<ProxyRuntime>,
    last_error: String,
}

pub struct ProxyManager {
    path: PathBuf,
    entries: RwLock<HashMap<String, ProxyEntry>>,
    operation_lock: Mutex<()>,
}

impl ProxyManager {
    pub fn load(path: PathBuf) -> Result<Self, String> {
        let configs = if path.exists() {
            let content = std::fs::read_to_string(&path)
                .map_err(|error| format!("Failed to read proxy config: {error}"))?;
            serde_json::from_str::<ProxyConfigFile>(&content)
                .map_err(|error| format!("Failed to parse proxy config: {error}"))?
                .proxies
        } else {
            Vec::new()
        };
        let entries = configs
            .into_iter()
            .map(|config| {
                (
                    config.id.clone(),
                    ProxyEntry {
                        config,
                        runtime: None,
                        last_error: String::new(),
                    },
                )
            })
            .collect();
        Ok(Self {
            path,
            entries: RwLock::new(entries),
            operation_lock: Mutex::new(()),
        })
    }

    pub async fn list(&self) -> ProxyListResponse {
        let entries = self.entries.read().await;
        let mut proxies = entries
            .values()
            .map(|entry| proxy_status(entry))
            .collect::<Vec<_>>();
        proxies.sort_by(|left, right| left.listen_port.cmp(&right.listen_port));
        ProxyListResponse { proxies }
    }

    pub async fn create(
        &self,
        conn: &Connection,
        request: ProxyUpsertRequest,
    ) -> Result<ProxyStatus, String> {
        let _operation = self.operation_lock.lock().await;
        find_modem_path_by_id(conn, request.modem_id.trim())
            .await
            .map_err(|error| error.to_string())?;
        let config = build_config(None, request, None)?;
        let id = config.id.clone();
        {
            let mut entries = self.entries.write().await;
            ensure_unique_listener(&entries, &config, None)?;
            entries.insert(
                id.clone(),
                ProxyEntry {
                    config,
                    runtime: None,
                    last_error: String::new(),
                },
            );
        }
        self.persist().await?;
        drop(_operation);

        let should_start = self
            .entries
            .read()
            .await
            .get(&id)
            .is_some_and(|entry| entry.config.enabled);
        if should_start {
            self.start(conn, &id).await?;
        }
        self.status(&id).await
    }

    pub async fn update(
        &self,
        conn: &Connection,
        id: &str,
        request: ProxyUpsertRequest,
    ) -> Result<ProxyStatus, String> {
        find_modem_path_by_id(conn, request.modem_id.trim())
            .await
            .map_err(|error| error.to_string())?;
        let was_running = self
            .entries
            .read()
            .await
            .get(id)
            .is_some_and(|entry| entry.runtime.is_some());
        if was_running {
            self.stop(id).await?;
        }

        let _operation = self.operation_lock.lock().await;
        let previous = self
            .entries
            .read()
            .await
            .get(id)
            .map(|entry| entry.config.clone())
            .ok_or_else(|| format!("Proxy instance not found: {id}"))?;
        let should_start = request.enabled;
        let config = build_config(Some(id.to_string()), request, Some(&previous))?;
        {
            let mut entries = self.entries.write().await;
            ensure_unique_listener(&entries, &config, Some(id))?;
            let entry = entries
                .get_mut(id)
                .ok_or_else(|| format!("Proxy instance not found: {id}"))?;
            entry.config = config;
            entry.last_error.clear();
        }
        self.persist().await?;
        drop(_operation);

        if should_start {
            self.start(conn, id).await?;
        }
        self.status(id).await
    }

    pub async fn delete(&self, id: &str) -> Result<(), String> {
        if self
            .entries
            .read()
            .await
            .get(id)
            .is_some_and(|entry| entry.runtime.is_some())
        {
            self.stop(id).await?;
        }
        let _operation = self.operation_lock.lock().await;
        let removed = self.entries.write().await.remove(id);
        if removed.is_none() {
            return Err(format!("Proxy instance not found: {id}"));
        }
        self.persist().await
    }

    pub async fn start(&self, conn: &Connection, id: &str) -> Result<ProxyStatus, String> {
        let _operation = self.operation_lock.lock().await;
        let config = {
            let entries = self.entries.read().await;
            let entry = entries
                .get(id)
                .ok_or_else(|| format!("Proxy instance not found: {id}"))?;
            if entry.runtime.is_some() {
                return Ok(proxy_status(entry));
            }
            entry.config.clone()
        };

        let network_interface = match modem_network_interface_by_id(conn, &config.modem_id).await {
            Ok(interface) => interface,
            Err(error) => {
                self.record_start_error(id, &error).await;
                return Err(error);
            }
        };
        let listener = match bind_proxy_listener(&config).await {
            Ok(listener) => listener,
            Err(error) => {
                let detail = format!(
                    "Failed to listen on {}:{}: {error}",
                    config.listen_host, config.listen_port
                );
                self.record_start_error(id, &detail).await;
                return Err(detail);
            }
        };
        let stats = Arc::new(ProxyStats::default());
        let (stop, stop_rx) = watch::channel(false);
        let task_config = config.clone();
        let task_interface = network_interface.clone();
        let task_stats = Arc::clone(&stats);
        let task = tokio::spawn(async move {
            run_proxy_listener(listener, task_config, task_interface, task_stats, stop_rx).await;
        });

        {
            let mut entries = self.entries.write().await;
            let entry = entries
                .get_mut(id)
                .ok_or_else(|| format!("Proxy instance not found: {id}"))?;
            entry.config.enabled = true;
            entry.last_error.clear();
            entry.runtime = Some(ProxyRuntime {
                network_interface: network_interface.clone(),
                started_at: Instant::now(),
                stats,
                stop,
                task,
            });
        }
        self.persist().await?;
        info!(proxy_id = id, interface = %network_interface, "Proxy instance started");
        self.status(id).await
    }

    pub async fn stop(&self, id: &str) -> Result<ProxyStatus, String> {
        let _operation = self.operation_lock.lock().await;
        let runtime = {
            let mut entries = self.entries.write().await;
            let entry = entries
                .get_mut(id)
                .ok_or_else(|| format!("Proxy instance not found: {id}"))?;
            entry.config.enabled = false;
            entry.runtime.take()
        };
        self.persist().await?;

        if let Some(runtime) = runtime {
            let _ = runtime.stop.send(true);
            let mut task = runtime.task;
            if tokio::time::timeout(std::time::Duration::from_secs(3), &mut task)
                .await
                .is_err()
            {
                task.abort();
            }
            info!(proxy_id = id, "Proxy instance stopped");
        }
        self.status(id).await
    }

    pub async fn start_enabled(&self, conn: &Connection) {
        let ids = self
            .entries
            .read()
            .await
            .values()
            .filter(|entry| entry.config.enabled)
            .map(|entry| entry.config.id.clone())
            .collect::<Vec<_>>();
        for id in ids {
            if let Err(error) = self.start(conn, &id).await {
                warn!(proxy_id = id, error = %error, "Failed to restore proxy instance");
            }
        }
    }

    async fn status(&self, id: &str) -> Result<ProxyStatus, String> {
        self.entries
            .read()
            .await
            .get(id)
            .map(proxy_status)
            .ok_or_else(|| format!("Proxy instance not found: {id}"))
    }

    async fn record_start_error(&self, id: &str, error: &str) {
        if let Some(entry) = self.entries.write().await.get_mut(id) {
            entry.config.enabled = false;
            entry.last_error = error.to_string();
        }
        let _ = self.persist().await;
    }

    async fn persist(&self) -> Result<(), String> {
        let configs = self
            .entries
            .read()
            .await
            .values()
            .map(|entry| entry.config.clone())
            .collect::<Vec<_>>();
        let payload = serde_json::to_vec_pretty(&ProxyConfigFile {
            version: PROXY_CONFIG_VERSION,
            proxies: configs,
        })
        .map_err(|error| format!("Failed to serialize proxy config: {error}"))?;
        if let Some(parent) = self.path.parent() {
            tokio::fs::create_dir_all(parent)
                .await
                .map_err(|error| format!("Failed to create proxy config directory: {error}"))?;
        }
        let temporary = self.path.with_extension("json.tmp");
        tokio::fs::write(&temporary, payload)
            .await
            .map_err(|error| format!("Failed to write proxy config: {error}"))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            tokio::fs::set_permissions(&temporary, std::fs::Permissions::from_mode(0o600))
                .await
                .map_err(|error| format!("Failed to secure proxy config: {error}"))?;
        }
        tokio::fs::rename(&temporary, &self.path)
            .await
            .map_err(|error| format!("Failed to replace proxy config: {error}"))
    }
}

fn build_config(
    id: Option<String>,
    request: ProxyUpsertRequest,
    previous: Option<&StoredProxyConfig>,
) -> Result<StoredProxyConfig, String> {
    let name = request.name.trim().to_string();
    let listen_host = request.listen_host.trim().to_string();
    let modem_id = request.modem_id.trim().to_string();
    let username = request.username.unwrap_or_default().trim().to_string();
    let password = match request.password {
        Some(password) => password,
        None => previous
            .map(|config| config.password.clone())
            .unwrap_or_default(),
    };
    let password = if username.is_empty() {
        String::new()
    } else {
        password
    };
    let config = StoredProxyConfig {
        id: id.unwrap_or_else(new_proxy_id),
        name,
        protocol: request.protocol,
        listen_host,
        listen_port: request.listen_port,
        modem_id,
        username,
        password,
        enabled: request.enabled,
    };
    validate_config(&config)?;
    Ok(config)
}

fn validate_config(config: &StoredProxyConfig) -> Result<(), String> {
    if config.name.is_empty() || config.name.len() > 64 {
        return Err("Proxy name must contain 1 to 64 characters".to_string());
    }
    let listen_ip = config
        .listen_host
        .parse::<IpAddr>()
        .map_err(|_| "Listen host must be an IPv4 or IPv6 address".to_string())?;
    if config.listen_port == 0 {
        return Err("Listen port must be greater than zero".to_string());
    }
    if config.modem_id.is_empty() || config.modem_id.len() > 256 {
        return Err("A valid modem id is required".to_string());
    }
    if config.username.len() > 255 || config.password.len() > 255 {
        return Err("Proxy credentials must not exceed 255 bytes".to_string());
    }
    if !config.username.is_empty() && config.password.is_empty() {
        return Err("A password is required when proxy authentication is enabled".to_string());
    }
    if !listen_ip.is_loopback() && (config.username.is_empty() || config.password.is_empty()) {
        return Err(
            "Authentication is required when listening on a non-loopback address".to_string(),
        );
    }
    Ok(())
}

fn ensure_unique_listener(
    entries: &HashMap<String, ProxyEntry>,
    config: &StoredProxyConfig,
    except_id: Option<&str>,
) -> Result<(), String> {
    let duplicate = entries.values().any(|entry| {
        Some(entry.config.id.as_str()) != except_id
            && entry.config.listen_host == config.listen_host
            && entry.config.listen_port == config.listen_port
    });
    if duplicate {
        Err(format!(
            "Another proxy already uses {}:{}",
            config.listen_host, config.listen_port
        ))
    } else {
        Ok(())
    }
}

fn new_proxy_id() -> String {
    format!(
        "proxy-{}-{}",
        chrono::Utc::now().timestamp_millis(),
        NEXT_PROXY_ID.fetch_add(1, Ordering::Relaxed)
    )
}

fn proxy_status(entry: &ProxyEntry) -> ProxyStatus {
    let runtime = entry.runtime.as_ref();
    let stats = runtime.map(|runtime| runtime.stats.as_ref());
    ProxyStatus {
        id: entry.config.id.clone(),
        name: entry.config.name.clone(),
        protocol: entry.config.protocol.as_str().to_string(),
        listen_host: entry.config.listen_host.clone(),
        listen_port: entry.config.listen_port,
        modem_id: entry.config.modem_id.clone(),
        network_interface: runtime
            .map(|runtime| runtime.network_interface.clone())
            .unwrap_or_default(),
        username: entry.config.username.clone(),
        has_password: !entry.config.password.is_empty(),
        enabled: entry.config.enabled,
        running: runtime.is_some(),
        total_connections: stats
            .map(|stats| stats.total_connections.load(Ordering::Relaxed))
            .unwrap_or_default(),
        active_connections: stats
            .map(|stats| stats.active_connections.load(Ordering::Relaxed))
            .unwrap_or_default(),
        bytes_uploaded: stats
            .map(|stats| stats.bytes_uploaded.load(Ordering::Relaxed))
            .unwrap_or_default(),
        bytes_downloaded: stats
            .map(|stats| stats.bytes_downloaded.load(Ordering::Relaxed))
            .unwrap_or_default(),
        errors: stats
            .map(|stats| stats.errors.load(Ordering::Relaxed))
            .unwrap_or_default(),
        uptime_seconds: runtime
            .map(|runtime| runtime.started_at.elapsed().as_secs())
            .unwrap_or_default(),
        last_error: entry.last_error.clone(),
    }
}

async fn bind_proxy_listener(config: &StoredProxyConfig) -> io::Result<TcpListener> {
    let ip = config
        .listen_host
        .parse::<IpAddr>()
        .map_err(|error| io::Error::new(io::ErrorKind::InvalidInput, error))?;
    TcpListener::bind(SocketAddr::new(ip, config.listen_port)).await
}

async fn run_proxy_listener(
    listener: TcpListener,
    config: StoredProxyConfig,
    network_interface: String,
    stats: Arc<ProxyStats>,
    mut stop: watch::Receiver<bool>,
) {
    loop {
        tokio::select! {
            changed = stop.changed() => {
                if changed.is_err() || *stop.borrow() {
                    break;
                }
            }
            accepted = listener.accept() => {
                let Ok((client, peer)) = accepted else {
                    stats.errors.fetch_add(1, Ordering::Relaxed);
                    continue;
                };
                let connection_config = config.clone();
                let interface = network_interface.clone();
                let connection_stats = Arc::clone(&stats);
                tokio::spawn(async move {
                    connection_stats.total_connections.fetch_add(1, Ordering::Relaxed);
                    connection_stats.active_connections.fetch_add(1, Ordering::Relaxed);
                    let result = match connection_config.protocol {
                        ProxyProtocol::Socks5 => handle_socks5(client, &connection_config, &interface).await,
                        ProxyProtocol::Http => handle_http(client, &connection_config, &interface).await,
                    };
                    match result {
                        Ok((uploaded, downloaded)) => {
                            connection_stats.bytes_uploaded.fetch_add(uploaded, Ordering::Relaxed);
                            connection_stats.bytes_downloaded.fetch_add(downloaded, Ordering::Relaxed);
                        }
                        Err(error) => {
                            connection_stats.errors.fetch_add(1, Ordering::Relaxed);
                            warn!(proxy_id = %connection_config.id, client = %peer, error = %error, "Proxy connection failed");
                        }
                    }
                    connection_stats.active_connections.fetch_sub(1, Ordering::Relaxed);
                });
            }
        }
    }
}

async fn connect_bound(host: &str, port: u16, interface: &str) -> io::Result<TcpStream> {
    let addresses = lookup_host((host, port)).await?.collect::<Vec<_>>();
    if addresses.is_empty() {
        return Err(io::Error::new(
            io::ErrorKind::NotFound,
            "Host did not resolve",
        ));
    }
    let mut last_error = None;
    for address in addresses {
        let socket = if address.is_ipv4() {
            TcpSocket::new_v4()?
        } else {
            TcpSocket::new_v6()?
        };
        if let Err(error) = SockRef::from(&socket).bind_device(Some(interface.as_bytes())) {
            return Err(io::Error::new(
                error.kind(),
                format!("SO_BINDTODEVICE {interface}: {error}"),
            ));
        }
        match socket.connect(address).await {
            Ok(stream) => {
                let _ = stream.set_nodelay(true);
                return Ok(stream);
            }
            Err(error) => last_error = Some(error),
        }
    }
    Err(last_error.unwrap_or_else(|| io::Error::other("Connection failed")))
}

fn credentials_match(config: &StoredProxyConfig, username: &[u8], password: &[u8]) -> bool {
    constant_time_eq(config.username.as_bytes(), username)
        && constant_time_eq(config.password.as_bytes(), password)
}

fn constant_time_eq(expected: &[u8], provided: &[u8]) -> bool {
    let mut difference = expected.len() ^ provided.len();
    let max_len = expected.len().max(provided.len());
    for index in 0..max_len {
        difference |= usize::from(
            expected.get(index).copied().unwrap_or_default()
                ^ provided.get(index).copied().unwrap_or_default(),
        );
    }
    difference == 0
}

async fn handle_socks5(
    mut client: TcpStream,
    config: &StoredProxyConfig,
    interface: &str,
) -> io::Result<(u64, u64)> {
    let mut greeting = [0_u8; 2];
    client.read_exact(&mut greeting).await?;
    if greeting[0] != 5 || greeting[1] as usize > MAX_SOCKS_METHODS {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "Invalid SOCKS5 greeting",
        ));
    }
    let mut methods = vec![0_u8; greeting[1] as usize];
    client.read_exact(&mut methods).await?;
    let required_method = if config.username.is_empty() {
        0x00
    } else {
        0x02
    };
    if !methods.contains(&required_method) {
        client.write_all(&[5, 0xff]).await?;
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "No acceptable SOCKS5 auth method",
        ));
    }
    client.write_all(&[5, required_method]).await?;

    if required_method == 0x02 {
        let mut auth_header = [0_u8; 2];
        client.read_exact(&mut auth_header).await?;
        if auth_header[0] != 1 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "Invalid SOCKS5 auth version",
            ));
        }
        let mut username = vec![0_u8; auth_header[1] as usize];
        client.read_exact(&mut username).await?;
        let password_len = client.read_u8().await? as usize;
        let mut password = vec![0_u8; password_len];
        client.read_exact(&mut password).await?;
        if !credentials_match(config, &username, &password) {
            client.write_all(&[1, 1]).await?;
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "SOCKS5 authentication failed",
            ));
        }
        client.write_all(&[1, 0]).await?;
    }

    let mut request = [0_u8; 4];
    client.read_exact(&mut request).await?;
    if request[0] != 5 || request[1] != 1 {
        write_socks_reply(&mut client, 0x07).await?;
        return Err(io::Error::new(
            io::ErrorKind::Unsupported,
            "Only SOCKS5 CONNECT is supported",
        ));
    }
    let host = match request[3] {
        1 => {
            let mut octets = [0_u8; 4];
            client.read_exact(&mut octets).await?;
            IpAddr::from(octets).to_string()
        }
        3 => {
            let length = client.read_u8().await? as usize;
            let mut domain = vec![0_u8; length];
            client.read_exact(&mut domain).await?;
            String::from_utf8(domain)
                .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "Invalid SOCKS5 domain"))?
        }
        4 => {
            let mut octets = [0_u8; 16];
            client.read_exact(&mut octets).await?;
            IpAddr::from(octets).to_string()
        }
        _ => {
            write_socks_reply(&mut client, 0x08).await?;
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "Unsupported SOCKS5 address type",
            ));
        }
    };
    let port = client.read_u16().await?;
    let mut upstream = match connect_bound(&host, port, interface).await {
        Ok(stream) => stream,
        Err(error) => {
            write_socks_reply(&mut client, 0x05).await?;
            return Err(error);
        }
    };
    write_socks_reply(&mut client, 0x00).await?;
    tokio::io::copy_bidirectional(&mut client, &mut upstream).await
}

async fn write_socks_reply(client: &mut TcpStream, status: u8) -> io::Result<()> {
    client.write_all(&[5, status, 0, 1, 0, 0, 0, 0, 0, 0]).await
}

async fn handle_http(
    mut client: TcpStream,
    config: &StoredProxyConfig,
    interface: &str,
) -> io::Result<(u64, u64)> {
    let header_bytes = read_http_headers(&mut client).await?;
    let mut headers = [httparse::EMPTY_HEADER; 64];
    let mut request = httparse::Request::new(&mut headers);
    let parsed = request
        .parse(&header_bytes)
        .map_err(|error| io::Error::new(io::ErrorKind::InvalidData, error))?;
    let header_len = match parsed {
        httparse::Status::Complete(length) => length,
        httparse::Status::Partial => {
            return Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "Incomplete HTTP request",
            ));
        }
    };
    let method = request.method.unwrap_or_default();
    let target = request.path.unwrap_or_default();
    let version = request.version.unwrap_or(1);

    if !config.username.is_empty() && !http_auth_matches(config, request.headers) {
        client
            .write_all(b"HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"SimAdmin\"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
            .await?;
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "HTTP proxy authentication failed",
        ));
    }

    let (host, port, origin_target) = resolve_http_target(method, target, request.headers)?;
    let mut upstream = match connect_bound(&host, port, interface).await {
        Ok(stream) => stream,
        Err(error) => {
            client
                .write_all(
                    b"HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
                )
                .await?;
            return Err(error);
        }
    };

    if method.eq_ignore_ascii_case("CONNECT") {
        client
            .write_all(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            .await?;
    } else {
        let mut forwarded = format!("{method} {origin_target} HTTP/1.{version}\r\n").into_bytes();
        for header in request.headers.iter() {
            if header.name.eq_ignore_ascii_case("proxy-authorization")
                || header.name.eq_ignore_ascii_case("proxy-connection")
            {
                continue;
            }
            forwarded.extend_from_slice(header.name.as_bytes());
            forwarded.extend_from_slice(b": ");
            forwarded.extend_from_slice(header.value);
            forwarded.extend_from_slice(b"\r\n");
        }
        forwarded.extend_from_slice(b"\r\n");
        forwarded.extend_from_slice(&header_bytes[header_len..]);
        upstream.write_all(&forwarded).await?;
    }

    tokio::io::copy_bidirectional(&mut client, &mut upstream).await
}

async fn read_http_headers(client: &mut TcpStream) -> io::Result<Vec<u8>> {
    let mut buffer = Vec::with_capacity(4096);
    loop {
        if buffer.windows(4).any(|window| window == b"\r\n\r\n") {
            return Ok(buffer);
        }
        if buffer.len() >= MAX_HTTP_HEADER_BYTES {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "HTTP headers are too large",
            ));
        }
        let mut chunk = [0_u8; 4096];
        let read = client.read(&mut chunk).await?;
        if read == 0 {
            return Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "HTTP client closed before headers",
            ));
        }
        buffer.extend_from_slice(&chunk[..read]);
    }
}

fn http_auth_matches(config: &StoredProxyConfig, headers: &[httparse::Header<'_>]) -> bool {
    let Some(header) = headers
        .iter()
        .find(|header| header.name.eq_ignore_ascii_case("proxy-authorization"))
    else {
        return false;
    };
    let Ok(value) = std::str::from_utf8(header.value) else {
        return false;
    };
    let Some(encoded) = value.trim().strip_prefix("Basic ") else {
        return false;
    };
    let Ok(decoded) = base64::engine::general_purpose::STANDARD.decode(encoded.trim()) else {
        return false;
    };
    let Some(separator) = decoded.iter().position(|byte| *byte == b':') else {
        return false;
    };
    credentials_match(config, &decoded[..separator], &decoded[separator + 1..])
}

fn resolve_http_target(
    method: &str,
    target: &str,
    headers: &[httparse::Header<'_>],
) -> io::Result<(String, u16, String)> {
    if method.eq_ignore_ascii_case("CONNECT") {
        let (host, port) = parse_host_port(target, 443)?;
        return Ok((host, port, target.to_string()));
    }
    if let Ok(uri) = target.parse::<Uri>() {
        if let Some(host) = uri.host() {
            let port = uri.port_u16().unwrap_or_else(|| {
                if uri.scheme_str() == Some("https") {
                    443
                } else {
                    80
                }
            });
            let origin = uri
                .path_and_query()
                .map(|value| value.as_str().to_string())
                .unwrap_or_else(|| "/".to_string());
            return Ok((host.to_string(), port, origin));
        }
    }
    let host_header = headers
        .iter()
        .find(|header| header.name.eq_ignore_ascii_case("host"))
        .and_then(|header| std::str::from_utf8(header.value).ok())
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidData, "HTTP Host header is missing"))?;
    let (host, port) = parse_host_port(host_header, 80)?;
    Ok((host, port, target.to_string()))
}

fn parse_host_port(value: &str, default_port: u16) -> io::Result<(String, u16)> {
    let value = value.trim();
    if value.is_empty() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "Proxy target is empty",
        ));
    }
    if value.starts_with('[') {
        let end = value
            .find(']')
            .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidInput, "Invalid IPv6 target"))?;
        let host = value[1..end].to_string();
        let port = value
            .get(end + 1..)
            .and_then(|suffix| suffix.strip_prefix(':'))
            .map(str::parse::<u16>)
            .transpose()
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "Invalid target port"))?
            .unwrap_or(default_port);
        return Ok((host, port));
    }
    if value.matches(':').count() == 1 {
        let (host, port) = value
            .rsplit_once(':')
            .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidInput, "Invalid proxy target"))?;
        let port = port
            .parse::<u16>()
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "Invalid target port"))?;
        return Ok((host.to_string(), port));
    }
    Ok((value.to_string(), default_port))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn config(host: &str, username: &str, password: &str) -> StoredProxyConfig {
        StoredProxyConfig {
            id: "proxy-test".to_string(),
            name: "Test".to_string(),
            protocol: ProxyProtocol::Socks5,
            listen_host: host.to_string(),
            listen_port: 1080,
            modem_id: "modem-test".to_string(),
            username: username.to_string(),
            password: password.to_string(),
            enabled: false,
        }
    }

    #[test]
    fn public_listener_requires_authentication() {
        assert!(validate_config(&config("0.0.0.0", "", "")).is_err());
        assert!(validate_config(&config("0.0.0.0", "user", "secret")).is_ok());
        assert!(validate_config(&config("127.0.0.1", "", "")).is_ok());
    }

    #[test]
    fn parses_host_and_ipv6_targets() {
        assert_eq!(
            parse_host_port("example.com:8443", 443).unwrap(),
            ("example.com".to_string(), 8443)
        );
        assert_eq!(
            parse_host_port("[2001:db8::1]:9443", 443).unwrap(),
            ("2001:db8::1".to_string(), 9443)
        );
        assert_eq!(
            parse_host_port("example.com", 80).unwrap(),
            ("example.com".to_string(), 80)
        );
    }

    #[test]
    fn verifies_http_basic_auth() {
        let config = config("127.0.0.1", "alice", "secret");
        let header = httparse::Header {
            name: "Proxy-Authorization",
            value: b"Basic YWxpY2U6c2VjcmV0",
        };
        assert!(http_auth_matches(&config, &[header]));
    }

    #[test]
    fn resolves_absolute_http_target() {
        let (host, port, target) =
            resolve_http_target("GET", "http://example.com:8080/a?q=1", &[]).unwrap();
        assert_eq!(host, "example.com");
        assert_eq!(port, 8080);
        assert_eq!(target, "/a?q=1");
    }

    #[test]
    fn constant_time_comparison_handles_different_lengths() {
        assert!(constant_time_eq(b"same", b"same"));
        assert!(!constant_time_eq(b"same", b"same-longer"));
        assert!(!constant_time_eq(b"same", b"diff"));
    }
}
