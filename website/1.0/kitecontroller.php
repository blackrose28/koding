<?php
require_once 'helpers.php';
require_once 'kitecluster.php';
require_once 'kite.php';

class KiteController {
  private $kites;
  
  function __construct ($config_path, $db) {
    $this->config_path = $config_path;
    $config_json = file_get_contents($config_path);
    if (empty($config_json)) {
      error_log("Invalid configuration: $config_path");
      return;
    }
    else {
      $this->config = $this->initialize_config($config_json);
      $this->db = $db;
    }
  }
  
  public function add_cluster ($kite_name, $cluster=NULL) {
    // trace($kite_name, $cluster);
    if (!isset($cluster)) {
      $cluster = $this->get_cluster($kite_name);
    }
    if (!isset($this->clusters[$kite_name])) {
      $this->clusters = array();
    }
    $cluster = new KiteCluster($kite_name, $cluster);
    trace('pushing', $kite_name, $cluster);
    array_push(
      $this->clusters[$kite_name],
      $cluster
    );
    trace('kluster name', $kite_name, count($this->clusters[$kite_name]));
    return count($this->clusters[$kite_name]);
  }
  
  public function get_cluster ($kite_name) {
    if (!isset($this->clusters[$kite_name])) {
      $this->clusters[$kite_name] = array();
    }
    return $this->clusters[$kite_name];
  }
  
  public function initialize_config ($config_json) {
    $config = json_decode($config_json);
    $db = get_mongo_db();
    foreach ($config->kites as $kite_name => $kite) {
      foreach ($kite->clusters as $cluster) {
        $this->add_cluster($kite_name, $cluster);
      }
    }
  }
  
  public function add_kite ($kite_name, $uri, $service_key=NULL) {
    $parsed_uri = parse_url($uri);
    $result = array('addedTo' => array());
    $clusters =& $this->get_cluster($kite_name);
    if (count($clusters) == 0) {
      if (isset($service_key)) {
        $db = get_mongo_db();
        $custom_cluster = $db->jKiteClusters->findOne(array(
          'kiteName' => $kite_name,
          'serviceKey' => $service_key,
        ));
        if (isset($custom_cluster)) {
          $custom_cluster = (object) $custom_cluster;
          $this->add_cluster($kite_name, $custom_cluster);
          $clusters =& $this->get_cluster($kite_name);
        }
      }
      if (!isset($clusters)) {
        error_log("Couldn't find a kite named: $kite_name.  ($service_key)");
        return FALSE;
      }
    }
    trace('klusters', $clusters);
    foreach ($clusters as $index=>$cluster) {
      trace('looking at a kluster', $index);
      if (
      ((  isset($service_key)
      &&  $cluster->trustPolicy->test('untrustedKite', $service_key))
      # this is a temporary measure to allow "trusted" kites to connect to the service
      ||  $cluster->trustPolicy->test('byHostname', $parsed_uri['host']
      ))
      && $cluster->add_kite($uri)
      ) {
        trace('in here captain');
        $pinger = $this->get_kite('pinger', 'kc');
        if (!isset($pinger)) {
          error_log('Pinger kite could not be reached!');
          return FALSE;
        }
        if ($kite_name != 'pinger') {
          $pinger->startPinging(array(
            'kiteName'  => $kite_name,
            'uri'       => $uri,
            'interval'  => 5000,
          ));
        }
        else {
          foreach ($this->clusters as $cluster) {
            foreach ($cluster as $node) {
              if ($node->name == 'pinger') {
                continue;
              }
              $kites = $node->get_kites();
              foreach ($kites as $kite) {
                $pinger->startPinging(array(
                  'kiteName' => $node->kite_name,
                  'uri' => $kite,
                  'interval' => 5000,
                ));
              }
            }
          }
        }
        array_push($result['addedTo'], $index);
      }
    }
    if (count($result['addedTo'])) {      
      error_log("kite was added to $kite_name clusters: ".implode(', ', $result['addedTo']));
      return TRUE;
    }
    return FALSE;
  }

  public function remove_kite ($kite_name, $uri) {
    error_log("forgetting $kite_name kite at $uri");
    $db = get_mongo_db();
    $result = $db->jKiteClusters->update(array(
      'kiteName' => $kite_name,
      'kites' => $uri,
    ), array(
      '$pull' => array(
        'kites' => $uri,
      ),
    ), array(
      'multiple' => TRUE,
    ));
    $db->jKiteConnections->remove(array(
      'kiteName' => $kite_name,
      'kiteUri'  => $uri,
    ));
  }

  private function get_next_kite_uri ($kite_name) {
    $clusters = $this->clusters[$kite_name];
    $cluster = $clusters[0];
    if (!isset($cluster)) {
      error_log("Cluster is not found: $kite_name");
    }
    $kite = $clusters[0]->get_next_kite_uri();
    if (!isset($kite)) {
      error_log('found no kites');
      return FALSE;
    }
    else {
      error_log('found a kite '.$kite);
      return $kite;
    }
  }

  public function get_kite_uri ($kite_name, $username) {
    $db = get_mongo_db();
    $connection = $db->jKiteConnections->findOne(array(
      'kiteName' => $kite_name,
      'username' => $username,
    ), array('kiteUri' => 1));
    if(!isset($connection)) {
      $kite_uri = $this->get_next_kite_uri($kite_name);
      if ($kite_uri) {
        $connection = array(
          'kiteName' => $kite_name,
          'username' => $username,
          'kiteUri' => $kite_uri,
        );
        $db->jKiteConnections->save($connection);        
      }
      else {
        return FALSE;
      }
    }
    return $connection['kiteUri'];
  }
  
  public function get_kite ($kite_name, $username) {
    return new Kite($kite_name, $this->get_kite_uri($kite_name, $username));
  }
}
